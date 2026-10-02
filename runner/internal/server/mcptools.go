package server

// The launch sheet's view of a person's MCP connections (spec 4.7, 6.3). All three routes are
// for a signed-in person only: Kind "human" with a live login session id, judged by the same
// checkGrantRequester the start route uses, so an agent token that holds session.start is
// refused. Connections come from core's account-scoped list, so another account's connection
// is indistinguishable from one that does not exist: 404.
//
// Listing a connection's tools goes through the gateway's own upstream code (mcpgw.ListTools:
// the netguard client, the credential core hands out for the caller's login session, the
// upstream handshake, every page of tools/list). Nothing is stored and the credential never
// leaves the gateway code.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// Bounds of the defaults body: core's own cap is 500 tools, and 128 KiB matches its PATCH limit.
const (
	mcpDefaultsBodyLimit = 128 << 10
	mcpMaxDefaultTools   = 500
)

// liveToolLister is the gateway's tool listing for the picker (mcpgw.Gateway.ListTools).
type liveToolLister interface {
	ListTools(ctx context.Context, proof mcpgw.Proof, connectionID, urlSnapshot string) ([]mcpgw.ListedTool, error)
}

// RegisterMCPRoutes registers the human-only MCP connection routes on mux.
func (a *API) RegisterMCPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mcp/connections", a.HandleGetMCPConnections)
	mux.HandleFunc("GET /api/mcp/connections/{id}/tools", a.HandleGetMCPConnectionTools)
	mux.HandleFunc("PATCH /api/mcp/connections/{id}/defaults", a.HandlePatchMCPConnectionDefaults)
}

// mcpConnectionView is one connection as the picker sees it. No secret is ever part of it.
type mcpConnectionView struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	URL          string                     `json:"url"`
	AuthKind     string                     `json:"auth_kind"`
	Status       string                     `json:"status"`
	DefaultTools map[string]mcpgw.ToolGrant `json:"default_tools"`
}

// mcpPersonAndConnections authenticates the caller as a signed-in person and lists their
// connections at core. It writes the refusal and returns false when it cannot.
func (a *API) mcpPersonAndConnections(w http.ResponseWriter, r *http.Request) (mcpgw.Proof, []MCPConnection, *mcpStartConfig, bool) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return mcpgw.Proof{}, nil, nil, false
	}
	who := requesterOf(principal)
	if apiErr := checkGrantRequester(who); apiErr != nil {
		writeAPIError(w, apiErr)
		return mcpgw.Proof{}, nil, nil, false
	}
	cfg := a.hub.mcpStart.get()
	if cfg == nil || cfg.Core == nil {
		writeError(w, http.StatusServiceUnavailable, msgMCPNoCore)
		return mcpgw.Proof{}, nil, nil, false
	}
	proof := mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}
	conns, err := cfg.Core.ListConnections(r.Context(), proof)
	switch {
	case errors.Is(err, ErrMCPProofInvalid):
		writeError(w, http.StatusUnauthorized, msgMCPSignIn)
		return mcpgw.Proof{}, nil, nil, false
	case err != nil:
		log.Printf("mcp connections: core list failed: %v", err)
		writeError(w, http.StatusBadGateway, "could not reach blerg-core to list MCP connections")
		return mcpgw.Proof{}, nil, nil, false
	}
	return proof, conns, cfg, true
}

// mcpConnectionByID finds one of the caller's connections, or writes the uniform 404.
func mcpConnectionByID(w http.ResponseWriter, conns []MCPConnection, id string) (MCPConnection, bool) {
	for _, c := range conns {
		if c.ID == id {
			return c, true
		}
	}
	writeError(w, http.StatusNotFound, msgMCPNotFound)
	return MCPConnection{}, false
}

// HandleGetMCPConnections returns the caller's connections: id, name, url, auth kind, status
// and default_tools.
func (a *API) HandleGetMCPConnections(w http.ResponseWriter, r *http.Request) {
	_, conns, _, ok := a.mcpPersonAndConnections(w, r)
	if !ok {
		return
	}
	out := make([]mcpConnectionView, 0, len(conns))
	for _, c := range conns {
		dt := c.DefaultTools
		if dt == nil {
			dt = map[string]mcpgw.ToolGrant{}
		}
		out = append(out, mcpConnectionView{ID: c.ID, Name: c.Name, URL: c.URL, AuthKind: c.AuthKind, Status: c.Status, DefaultTools: dt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

// HandleGetMCPConnectionTools lists a connection's tools from the live upstream: name,
// description, input schema, annotations and the hash a launch will pin (spec 6.3).
func (a *API) HandleGetMCPConnectionTools(w http.ResponseWriter, r *http.Request) {
	proof, conns, cfg, ok := a.mcpPersonAndConnections(w, r)
	if !ok {
		return
	}
	conn, ok := mcpConnectionByID(w, conns, r.PathValue("id"))
	if !ok {
		return
	}
	if conn.Status != "ok" {
		writeError(w, http.StatusConflict, "this connection is not ready ("+conn.Status+"): fix it in blerg-core settings")
		return
	}
	lister, _ := cfg.Hasher.(liveToolLister)
	if lister == nil {
		writeError(w, http.StatusServiceUnavailable, msgMCPNoGateway)
		return
	}
	tools, err := lister.ListTools(r.Context(), proof, conn.ID, conn.URL)
	switch {
	case errors.Is(err, mcpgw.ErrConnectionGone):
		writeError(w, http.StatusNotFound, msgMCPNotFound)
		return
	case err != nil:
		// The error can name hosts and addresses: log it, never send it.
		log.Printf("mcp tools: listing %s failed: %v", conn.ID, err)
		writeError(w, http.StatusBadGateway, "could not list this connection's tools: the MCP server was unreachable or refused the request")
		return
	}
	if tools == nil {
		tools = []mcpgw.ListedTool{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": tools})
}

// HandlePatchMCPConnectionDefaults saves a connection's default_tools through core's internal
// defaults route, with the caller's login session as the proof, after the same person and
// ownership checks as the other routes. The body is {"default_tools": {tool: {mode, hash}}}; an
// empty object clears the defaults. Core validates again and its message is passed on.
func (a *API) HandlePatchMCPConnectionDefaults(w http.ResponseWriter, r *http.Request) {
	proof, conns, cfg, ok := a.mcpPersonAndConnections(w, r)
	if !ok {
		return
	}
	conn, ok := mcpConnectionByID(w, conns, r.PathValue("id"))
	if !ok {
		return
	}
	var body struct {
		DefaultTools map[string]mcpgw.ToolGrant `json:"default_tools"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, mcpDefaultsBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.DefaultTools == nil {
		writeError(w, http.StatusBadRequest, "the body must be {\"default_tools\": {<tool>: {\"mode\": ..., \"hash\": ...}}}")
		return
	}
	if len(body.DefaultTools) > mcpMaxDefaultTools {
		writeError(w, http.StatusBadRequest, "too many tools")
		return
	}
	for name, g := range body.DefaultTools {
		if name == "" || (g.Mode != mcpgw.ModeAllow && g.Mode != mcpgw.ModePropose) || g.Hash == "" {
			writeError(w, http.StatusBadRequest, "each tool needs a name, a mode of allow or propose and a hash")
			return
		}
	}
	setter, _ := cfg.Core.(mcpDefaultsSetter)
	if setter == nil {
		writeError(w, http.StatusServiceUnavailable, msgMCPNoCore)
		return
	}
	err := setter.SetDefaultTools(r.Context(), proof, conn.ID, body.DefaultTools)
	var invalid *mcpDefaultsInvalid
	switch {
	case errors.Is(err, ErrMCPProofInvalid):
		writeError(w, http.StatusUnauthorized, msgMCPSignIn)
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, invalid.Msg)
	case err != nil:
		log.Printf("mcp connections: core defaults update failed: %v", err)
		writeError(w, http.StatusBadGateway, "could not reach blerg-core to save the default tools")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"default_tools": body.DefaultTools})
	}
}
