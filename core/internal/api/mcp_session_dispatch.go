package api

import (
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
	"io"
	"log/slog"
	"net/http"
)

func sessionDispatchHTTPFailure(cause string) {
	failurelog.Default.Event("mcp_session_dispatch_http:"+cause, slog.LevelWarn, "component", "core", "operation", "mcp_session_dispatch_http", "cause", cause)
}
func (d Deps) handleInternalSessionMCPAdmit(w http.ResponseWriter, r *http.Request) {
	d.handleInternalSessionMCPDispatch(w, r, false)
}
func (d Deps) handleInternalSessionMCPToken(w http.ResponseWriter, r *http.Request) {
	d.handleInternalSessionMCPDispatch(w, r, true)
}
func (d Deps) handleInternalSessionMCPDispatch(w http.ResponseWriter, r *http.Request, token bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if d.InternalKey == "" {
		sessionDispatchHTTPFailure("unconfigured")
		http.Error(w, "Remote MCP dispatch is not available.", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		sessionDispatchHTTPFailure("internal_key_refused")
		http.Error(w, "Unauthorized.", http.StatusUnauthorized)
		return
	}
	if d.MCPConnections == nil {
		sessionDispatchHTTPFailure("unconfigured")
		http.Error(w, "Remote MCP dispatch is not available.", http.StatusServiceUnavailable)
		return
	}
	var in mcpconn.SessionDispatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, internalMCPBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		sessionDispatchHTTPFailure("invalid_body")
		http.Error(w, "Request body is not valid.", http.StatusBadRequest)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		sessionDispatchHTTPFailure("invalid_body")
		http.Error(w, "Request body is not valid.", http.StatusBadRequest)
		return
	}
	var res mcpconn.SecretResult
	var err error
	if token {
		res, err = d.MCPConnections.FetchSessionSecret(r.Context(), in)
	} else {
		err = d.MCPConnections.CheckSessionDispatch(r.Context(), in)
	}
	if err != nil {
		sessionDispatchHTTPFailure(mcpconn.FailureCause(err))
		switch {
		case errors.Is(err, mcpconn.ErrSessionRefreshBusy):
			w.Header().Set("Retry-After", "1")
			writeJSONStatus(w, http.StatusServiceUnavailable, struct {
				Error        string `json:"error"`
				RetryAfterMS int    `json:"retry_after_ms"`
			}{"refresh_in_progress", 100})
		case errors.Is(err, mcpconn.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, mcpconn.ErrUnavailable):
			http.Error(w, "Remote MCP dispatch is temporarily unavailable.", http.StatusServiceUnavailable)
		default:
			http.Error(w, "Remote MCP dispatch could not be checked.", http.StatusInternalServerError)
		}
		return
	}
	if token {
		writeJSON(w, internalMCPTokenResponse{URL: res.URL, HeaderName: res.HeaderName, Value: res.Value, ExpiresAt: res.ExpiresAt, PinPolicy: res.PinPolicy})
		return
	}
	writeJSON(w, struct {
		Live bool `json:"live"`
	}{true})
}

// Lifecycle control is internal-key only and cannot attach or obtain credentials.
func (d Deps) handleInternalSessionMCPLifecycle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if d.InternalKey == "" || d.MCPConnections == nil {
		sessionDispatchHTTPFailure("unconfigured")
		http.Error(w, "Remote MCP lifecycle is not available.", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		sessionDispatchHTTPFailure("internal_key_refused")
		http.Error(w, "Unauthorized.", http.StatusUnauthorized)
		return
	}
	var in struct {
		mcpconn.SessionDispatchRequest
		Action string `json:"action"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, internalMCPBodyLimit))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		sessionDispatchHTTPFailure("invalid_body")
		http.Error(w, "Request body is not valid.", http.StatusBadRequest)
		return
	}
	var err error
	generation := in.CredentialGeneration
	switch in.Action {
	case "renew":
		err = d.MCPConnections.RenewSessionBinding(r.Context(), in.SessionDispatchRequest)
	case "suspend":
		var c mcpconn.SessionCredential
		c, err = d.MCPConnections.SuspendSessionCredential(r.Context(), in.SessionScope, in.ConnectionID, in.CredentialGeneration)
		generation = c.Generation
	case "close":
		err = d.MCPConnections.CloseSessionCredential(r.Context(), in.SessionScope, in.ConnectionID, in.CredentialGeneration)
	default:
		sessionDispatchHTTPFailure("invalid_action")
		http.Error(w, "Lifecycle action is not valid.", http.StatusBadRequest)
		return
	}
	if err != nil {
		sessionDispatchHTTPFailure(mcpconn.FailureCause(err))
		if errors.Is(err, mcpconn.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, "Remote MCP lifecycle is temporarily unavailable.", http.StatusServiceUnavailable)
		}
		return
	}
	writeJSON(w, struct {
		Applied    bool  `json:"applied"`
		Generation int64 `json:"credential_generation"`
	}{true, generation})
}
