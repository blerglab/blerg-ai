package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

// MCP connections (docs/design/ai-crons.md 4.4, 4.5): a person's remote MCP servers. The public
// routes are the signed-in person's own (human-only, same-origin); the internal routes are how the
// runner reads them for a session. No response here ever carries a secret except the internal
// token route, which hands over exactly one connection's credential and only after an audit row
// was written.

// Public request body caps; the largest legitimate body is a default_tools map. The service
// accepts up to 500 tools with 200-character names and 128-character hashes (about 185 KB of
// compact JSON), so the PATCH cap sits well above that: a payload the service would accept must
// never be refused as "too large" first (a test pins the largest one).
const (
	mcpCreateBodyLimit = 16 << 10
	mcpPatchBodyLimit  = 512 << 10
)

// redactMCPURL hides what an older connection row may hold after the URL: a query string (a key
// put there instead of in the header field) and a fragment. The rule against both was added
// after some rows existed, and the values must not come back through the API or be copied into
// anything derived from a list. It returns the URL with a bare "?…" marker in place of a query
// and reports whether anything was hidden. Only the internal token route, which the runner's
// gateway needs, returns the stored URL.
func redactMCPURL(raw string) (string, bool) {
	i := strings.IndexAny(raw, "?#")
	if i < 0 {
		return raw, false
	}
	if raw[i] == '?' {
		return raw[:i] + "?…", true
	}
	return raw[:i], true
}

// mcpConnectionView is a Connection as the public API returns it: the URL redacted, has_query set
// when something was hidden.
type mcpConnectionView struct {
	mcpconn.Connection
	URL      string `json:"url"`
	HasQuery bool   `json:"has_query,omitempty"`
}

func viewMCPConnection(c mcpconn.Connection) mcpConnectionView {
	u, hidden := redactMCPURL(c.URL)
	return mcpConnectionView{Connection: c, URL: u, HasQuery: hidden}
}

type mcpCreateRequest struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	AuthKind   string `json:"auth_kind"`
	HeaderName string `json:"header_name"`
	Secret     string `json:"secret"`
}

type mcpPatchRequest struct {
	Name         *string         `json:"name"`
	DefaultTools json.RawMessage `json:"default_tools"`
	Secret       *string         `json:"secret"`
	HeaderName   *string         `json:"header_name"`
}

type mcpListResponse struct {
	Connections []mcpConnectionView `json:"connections"`
	Max         int                 `json:"max"`
}

// decodeMCPBody reads a capped, strict JSON body into v and writes the error response itself,
// returning false. The body is never logged: it may hold a secret.
func decodeMCPBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return false
	}
	return true
}

func writeMCPError(w http.ResponseWriter, err error) {
	var ve *mcpconn.ValidationError
	switch {
	case errors.As(err, &ve):
		http.Error(w, ve.Msg, http.StatusBadRequest)
	case errors.Is(err, mcpconn.ErrNameTaken):
		http.Error(w, "another connection already uses this name", http.StatusConflict)
	case errors.Is(err, mcpconn.ErrLimit):
		http.Error(w, "connection limit reached", http.StatusConflict)
	case errors.Is(err, mcpconn.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, mcpconn.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many connection attempts: try again in a minute", http.StatusTooManyRequests)
	default:
		log.Printf("mcp connections: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (d Deps) handleListMCPConnections(w http.ResponseWriter, r *http.Request) {
	if d.MCPConnections == nil {
		http.Error(w, "mcp connections unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	list, err := d.MCPConnections.List(r.Context(), principal.Sub)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	views := make([]mcpConnectionView, 0, len(list))
	for _, c := range list {
		views = append(views, viewMCPConnection(c))
	}
	writeJSON(w, mcpListResponse{Connections: views, Max: mcpconn.MaxPerAccount})
}

// handleCreateMCPConnection is POST /api/mcp/connections. Same-origin guarded like POST
// /api/tokens: it stores a credential off the caller's browser session.
func (d Deps) handleCreateMCPConnection(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	if d.MCPConnections == nil {
		http.Error(w, "mcp connections unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	var body mcpCreateRequest
	if !decodeMCPBody(w, r, mcpCreateBodyLimit, &body) {
		return
	}
	c, err := d.MCPConnections.Create(r.Context(), principal.Sub, mcpconn.CreateInput{
		Name: body.Name, URL: body.URL, AuthKind: body.AuthKind, HeaderName: body.HeaderName, Secret: body.Secret,
	})
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, viewMCPConnection(c))
}

// handlePatchMCPConnection is PATCH /api/mcp/connections/{id}: rename, default_tools, replace a
// static secret or its header name.
func (d Deps) handlePatchMCPConnection(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	if d.MCPConnections == nil {
		http.Error(w, "mcp connections unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	var body mcpPatchRequest
	if !decodeMCPBody(w, r, mcpPatchBodyLimit, &body) {
		return
	}
	c, err := d.MCPConnections.Patch(r.Context(), principal.Sub, r.PathValue("id"), mcpconn.PatchInput{
		Name: body.Name, DefaultTools: body.DefaultTools, Secret: body.Secret, HeaderName: body.HeaderName,
	})
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeJSON(w, viewMCPConnection(c))
}

// handleDeleteMCPConnection is DELETE /api/mcp/connections/{id}. Another account's id is the same
// 404 as an unknown one.
func (d Deps) handleDeleteMCPConnection(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	if d.MCPConnections == nil {
		http.Error(w, "mcp connections unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	if err := d.MCPConnections.Delete(r.Context(), principal.Sub, r.PathValue("id")); err != nil {
		writeMCPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// internalMCPProof is the mandatory liveness proof of every internal route, exactly as on the
// credential and plugin endpoints: the account, and exactly one of token_id / session_id.
type internalMCPProof struct {
	AccountID string `json:"account_id"`
	TokenID   string `json:"token_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// internalMCPListRequest is POST /internal/mcp/connections/list's body.
type internalMCPListRequest struct {
	internalMCPProof
}

type internalMCPConnection struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	URL          string          `json:"url"`
	HasQuery     bool            `json:"has_query,omitempty"`
	AuthKind     string          `json:"auth_kind"`
	Status       string          `json:"status"`
	DefaultTools json.RawMessage `json:"default_tools"`
}

// newInternalMCPConnection is the runner-facing view of a connection (no secret, URL redacted).
func newInternalMCPConnection(c mcpconn.Connection) internalMCPConnection {
	u, hidden := redactMCPURL(c.URL)
	return internalMCPConnection{
		ID: c.ID, Name: c.Name, URL: u, HasQuery: hidden, AuthKind: c.AuthKind, Status: c.Status, DefaultTools: c.DefaultTools,
	}
}

type internalMCPListResponse struct {
	Connections []internalMCPConnection `json:"connections"`
}

// internalMCPTokenRequest is POST /internal/mcp/connections/token's body.
type internalMCPTokenRequest struct {
	internalMCPProof
	ConnectionID string `json:"connection_id"`
}

type internalMCPTokenResponse struct {
	URL        string     `json:"url"`
	HeaderName string     `json:"header_name,omitempty"`
	Value      string     `json:"value,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// internalMCPGate runs the checks shared by both internal routes: the internal key, service
// availability, a capped body decoded into body (which embeds proof), the structural checks and
// the liveness proof. It writes the response itself and returns ok=false on any failure. On
// success it returns the audit identity (session or token) of the principal behind the call.
// extra, when set, is a further structural check returning a 400 message.
func (d Deps) internalMCPGate(w http.ResponseWriter, r *http.Request, body any, proof *internalMCPProof, extra func() string) (mcpconn.Proof, bool) {
	return d.internalMCPGateLimit(w, r, body, proof, extra, internalMCPBodyLimit)
}

// internalMCPBodyLimit is the default cap of an internal route's body.
const internalMCPBodyLimit = 16 << 10

// internalMCPGateLimit is internalMCPGate with an explicit body cap.
func (d Deps) internalMCPGateLimit(w http.ResponseWriter, r *http.Request, body any, proof *internalMCPProof, extra func() string, limit int64) (mcpconn.Proof, bool) {
	if d.InternalKey == "" {
		http.Error(w, "internal mcp connections disabled", http.StatusServiceUnavailable)
		return mcpconn.Proof{}, false
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return mcpconn.Proof{}, false
	}
	if d.MCPConnections == nil || d.Store == nil {
		http.Error(w, "mcp connections unavailable", http.StatusServiceUnavailable)
		return mcpconn.Proof{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return mcpconn.Proof{}, false
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return mcpconn.Proof{}, false
	}
	if msg := validateProofShape(proof.AccountID, proof.TokenID, proof.SessionID); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return mcpconn.Proof{}, false
	}
	if extra != nil {
		if msg := extra(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return mcpconn.Proof{}, false
		}
	}
	sid, tid, ok, err := d.principalLiveness(r.Context(), proof.AccountID, proof.TokenID, proof.SessionID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return mcpconn.Proof{}, false
	}
	if !ok {
		log.Printf("internal mcp: no live principal for account %s", proof.AccountID)
		http.Error(w, "not found", http.StatusNotFound)
		return mcpconn.Proof{}, false
	}
	return mcpconn.Proof{SessionID: sid, TokenID: tid}, true
}

// handleInternalListMCPConnections is POST /internal/mcp/connections/list: the account's
// connections for the runner (no secrets).
func (d Deps) handleInternalListMCPConnections(w http.ResponseWriter, r *http.Request) {
	var body internalMCPListRequest
	if _, ok := d.internalMCPGate(w, r, &body, &body.internalMCPProof, nil); !ok {
		return
	}
	list, err := d.MCPConnections.List(r.Context(), body.AccountID)
	if err != nil {
		log.Printf("internal mcp list: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]internalMCPConnection, 0, len(list))
	for _, c := range list {
		out = append(out, newInternalMCPConnection(c))
	}
	writeJSON(w, internalMCPListResponse{Connections: out})
}

// handleInternalMCPToken is POST /internal/mcp/connections/token. Gated like the credential
// fetch: the internal key plus a live proof. Every failure past the shape checks, including
// another account's connection, is the uniform 404. The audit row is written first (inside
// FetchSecret) and a failure to write it aborts without returning the secret.
func (d Deps) handleInternalMCPToken(w http.ResponseWriter, r *http.Request) {
	var body internalMCPTokenRequest
	proof, ok := d.internalMCPGate(w, r, &body, &body.internalMCPProof, func() string {
		if !uuidShape.MatchString(body.ConnectionID) {
			return "connection_id must be a uuid"
		}
		return ""
	})
	if !ok {
		return
	}
	res, err := d.MCPConnections.FetchSecret(r.Context(), body.AccountID, body.ConnectionID, proof)
	if err != nil {
		if errors.Is(err, mcpconn.ErrNotFound) {
			// Also what an OAuth connection in needs_auth (or whose refresh was rejected) answers:
			// the runner treats it as "connection unavailable, sign in again".
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, mcpconn.ErrUnavailable) {
			// The provider could not be reached to refresh an expired token: retry later.
			w.Header().Set("Retry-After", "30")
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		log.Printf("internal mcp token: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, internalMCPTokenResponse{URL: res.URL, HeaderName: res.HeaderName, Value: res.Value, ExpiresAt: res.ExpiresAt})
}
