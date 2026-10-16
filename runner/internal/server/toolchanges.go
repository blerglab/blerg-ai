package server

// Tool changes (docs/design/mcp-tool-changes.md §5): what a signed-in person does when an MCP
// server changed a tool a session pinned, offers one the launch never saw, or stopped offering
// one. The gateway recorded the change (mcpgw/drift.go); here the owner reads the two definitions
// and accepts (re-pin or grant, in place) or declines. Every route is human only and owner only,
// as the proposals routes are, and an accept is proven by the person's own login session.
//
// Accept, in order: the row must be pending and the mode may only tighten; the new definition
// must be stored, reviewable and hash to the row's new_hash; the connection must still exist for
// this person at the grant's address and list the tool at that very hash; then, in ONE
// transaction, the row is accepted, the grant entry is re-pinned or granted (compare-and-set) and
// the pin event is written. Afterwards the daemon is asked to have the agent list its tools again.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const (
	toolChangeBodyLimit   = 1 << 10
	msgToolChangeNotFound = "tool change not found"
)

type toolChangesHandler struct{ p *proposalsHandler }

// definitionView is one stored definition as the page shows it: the parsed name, description
// and input schema (as raw JSON text), the canonical bytes, and why it cannot be reviewed.
type definitionView struct {
	Hash        string          `json:"hash"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Canonical   string          `json:"canonical"`
	Unsafe      string          `json:"unsafe,omitempty"`
}

func viewOfDefinition(d *db.ToolDefinition) *definitionView {
	if d == nil {
		return nil
	}
	v := &definitionView{Hash: d.Hash, Name: d.Name, Canonical: d.Canonical, Unsafe: d.Unsafe}
	var parsed struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if d.Canonical != "" && json.Unmarshal([]byte(d.Canonical), &parsed) == nil {
		v.Description, v.InputSchema = parsed.Description, parsed.InputSchema
	}
	if len(v.InputSchema) == 0 {
		v.InputSchema = json.RawMessage("null")
	}
	return v
}

type toolChangeView struct {
	ID             string          `json:"id"`
	State          string          `json:"state"`
	Kind           string          `json:"kind"`
	ConnectionID   string          `json:"connection_id"`
	ConnectionName string          `json:"connection_name"`
	Tool           string          `json:"tool"`
	OldHash        string          `json:"old_hash"`
	NewHash        string          `json:"new_hash"`
	Mode           string          `json:"mode"`
	Muted          bool            `json:"muted"`
	SessionID      string          `json:"session_id"`
	CreatedAt      time.Time       `json:"created_at"`
	ExpiresAt      time.Time       `json:"expires_at"`
	DecidedAt      *time.Time      `json:"decided_at"`
	DecidedBy      *string         `json:"decided_by"`
	OldDefinition  *definitionView `json:"old_definition,omitempty"`
	NewDefinition  *definitionView `json:"new_definition,omitempty"`
}

func viewOfToolChange(c *db.ToolChange) toolChangeView {
	return toolChangeView{
		ID: c.ID, State: c.State, Kind: c.Kind, ConnectionID: c.ConnectionID, ConnectionName: c.ConnectionName, Tool: c.Tool,
		OldHash: c.OldHash, NewHash: c.NewHash, Mode: c.Mode, Muted: c.Muted, SessionID: c.SessionID,
		CreatedAt: c.CreatedAt, ExpiresAt: c.CreatedAt.Add(db.ToolChangeExpiry), DecidedAt: c.DecidedAt, DecidedBy: c.DecidedBy,
	}
}

func (h *toolChangesHandler) owned(w http.ResponseWriter, r *http.Request, who grantRequester) (*db.ToolChange, bool) {
	c, err := db.GetOwnedToolChange(r.Context(), h.p.api.dbPool, who.Account, r.PathValue("id"))
	switch {
	case errors.Is(err, db.ErrToolChangeNotFound):
		writeError(w, http.StatusNotFound, msgToolChangeNotFound)
		return nil, false
	case err != nil:
		log.Printf("tool changes: read: %v", err)
		writeError(w, http.StatusInternalServerError, "could not read the tool change")
		return nil, false
	}
	return c, true
}

// list answers GET /api/tool-changes?scope=open|decided&session=&before=&limit=.
func (h *toolChangesHandler) list(w http.ResponseWriter, r *http.Request) {
	who, ok := h.p.person(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	scope := q.Get("scope")
	if scope == "" {
		scope = "open"
	}
	limit := proposalDecidedPage
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, proposalDecidedMax)
	}
	var rows []db.ToolChange
	var err error
	var next *string
	switch scope {
	case "open":
		rows, err = db.ListOpenToolChanges(r.Context(), h.p.api.dbPool, who.Account, q.Get("session"))
	case "decided":
		var cursor string
		rows, cursor, err = db.ListDecidedToolChanges(r.Context(), h.p.api.dbPool, who.Account, q.Get("session"), q.Get("before"), limit)
		if cursor != "" {
			next = &cursor
		}
	default:
		writeError(w, http.StatusBadRequest, "unknown scope: use open or decided")
		return
	}
	if errors.Is(err, db.ErrToolChangeCursor) {
		writeError(w, http.StatusBadRequest, "before is not a cursor this list returned")
		return
	}
	if err != nil {
		log.Printf("tool changes: list: %v", err)
		writeError(w, http.StatusInternalServerError, "could not list tool changes")
		return
	}
	pending, err := db.CountPendingToolChanges(r.Context(), h.p.api.dbPool, who.Account)
	if err != nil {
		log.Printf("tool changes: count: %v", err)
		writeError(w, http.StatusInternalServerError, "could not list tool changes")
		return
	}
	out := make([]toolChangeView, 0, len(rows))
	for i := range rows {
		out = append(out, viewOfToolChange(&rows[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tool_changes": out, "decided_next": next, "pending_count": pending})
}

func (h *toolChangesHandler) get(w http.ResponseWriter, r *http.Request) {
	who, ok := h.p.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, who)
	if !ok {
		return
	}
	v := viewOfToolChange(c)
	if c.OldHash != "" {
		if d, err := db.GetToolDefinition(r.Context(), h.p.api.dbPool, c.OldHash); err == nil {
			v.OldDefinition = viewOfDefinition(d)
		}
	}
	if c.NewHash != "" {
		if d, err := db.GetToolDefinition(r.Context(), h.p.api.dbPool, c.NewHash); err == nil {
			v.NewDefinition = viewOfDefinition(d)
		}
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *toolChangesHandler) decline(w http.ResponseWriter, r *http.Request) {
	who, ok := h.p.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, who)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), proposalWriteBudget)
	defer cancel()
	// One transaction: the row and its `declined` pin event (db.DeclineToolChangeWithEvent).
	done, err := db.DeclineToolChangeWithEvent(ctx, h.p.api.dbPool, who.Account, c.ID, who.Account)
	if err != nil {
		transitionToolChangeError(w, err)
		return
	}
	log.Printf("tool changes: declined id=%s account=%s connection=%s tool=%q", c.ID, c.AccountID, c.ConnectionID, c.Tool)
	writeJSON(w, http.StatusOK, viewOfToolChange(done))
}

func transitionToolChangeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrToolChangeNotFound):
		writeError(w, http.StatusNotFound, msgToolChangeNotFound)
	case errors.Is(err, db.ErrToolChangeState):
		writeError(w, http.StatusConflict, "this tool change is no longer pending")
	default:
		log.Printf("tool changes: transition: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update the tool change")
	}
}

func (h *toolChangesHandler) accept(w http.ResponseWriter, r *http.Request) {
	who, ok := h.p.person(w, r)
	if !ok {
		return
	}
	c, ok := h.owned(w, r, who)
	if !ok {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, toolChangeBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || (body.Mode != mcpgw.ModeAllow && body.Mode != mcpgw.ModePropose) {
		writeError(w, http.StatusBadRequest, `the body must be {"mode": "allow"} or {"mode": "propose"}`)
		return
	}
	if c.State == db.ToolChangeAccepted {
		writeJSON(w, http.StatusOK, viewOfToolChange(c)) // idempotent
		return
	}
	if c.State != db.ToolChangePending {
		writeError(w, http.StatusConflict, "this tool change is no longer pending ("+c.State+")")
		return
	}
	if c.Kind == db.ToolChangeRemoved {
		writeError(w, http.StatusConflict, "the server no longer offers this tool; there is nothing to accept")
		return
	}
	if body.Mode == mcpgw.ModeAllow && c.Mode != mcpgw.ModeAllow {
		writeError(w, http.StatusBadRequest, "this change was recorded as propose; accepting may keep or tighten the mode, never widen it")
		return
	}
	// The definition the person saw must be the one that gets pinned.
	def, err := db.GetToolDefinition(r.Context(), h.p.api.dbPool, c.NewHash)
	if err != nil {
		writeError(w, http.StatusConflict, "the new definition is not stored, so it cannot be accepted; wait for the next listing")
		return
	}
	if def.Unsafe != "" {
		writeError(w, http.StatusConflict, "this definition cannot be reviewed ("+def.Unsafe+") and so cannot be accepted")
		return
	}
	if sum := sha256.Sum256([]byte(def.Canonical)); hex.EncodeToString(sum[:]) != c.NewHash {
		writeError(w, http.StatusConflict, "the stored definition does not hash to the recorded change")
		return
	}
	// The grant row: gone means the session ended.
	grants, err := db.ListMCPGrantsForSession(r.Context(), h.p.api.dbPool, c.SessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the session's connections")
		return
	}
	var grant *db.MCPGrant
	for i := range grants {
		if grants[i].ConnectionID == c.ConnectionID {
			grant = &grants[i]
		}
	}
	if grant == nil {
		// The session ended: the row expires now, with its `expired` event, in one transaction.
		ectx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), proposalWriteBudget)
		defer cancel()
		if _, err := db.ExpireToolChangeWithEvent(ectx, h.p.api.dbPool, who.Account, c.ID); err != nil {
			log.Printf("tool changes: expire %s: %v", c.ID, err)
		}
		writeError(w, http.StatusGone, "the session has ended, so this change can no longer be accepted")
		return
	}
	// The connection, as proposal approval checks it, and the live hash.
	cfg := h.p.api.hub.mcpStart.get()
	if cfg == nil || cfg.Core == nil || cfg.Hasher == nil {
		writeError(w, http.StatusServiceUnavailable, msgProposalNoGW)
		return
	}
	proof := mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}
	conns, err := cfg.Core.ListConnections(r.Context(), proof)
	switch {
	case errors.Is(err, ErrMCPProofInvalid):
		writeError(w, http.StatusUnauthorized, msgProposalSignIn)
		return
	case err != nil:
		log.Printf("tool changes: core list failed: %v", err)
		writeError(w, http.StatusBadGateway, "could not reach blerg-core to check the connection: try again shortly")
		return
	}
	var conn *MCPConnection
	for i := range conns {
		if conns[i].ID == c.ConnectionID {
			conn = &conns[i]
		}
	}
	switch {
	case conn == nil:
		writeError(w, http.StatusConflict, "the connection no longer exists")
		return
	case conn.URL != grant.URLSnapshot:
		writeError(w, http.StatusConflict, "the connection's address changed since the session started")
		return
	case conn.Status != "ok":
		writeError(w, http.StatusConflict, "the connection needs attention ("+conn.Status+"): fix it in blerg-core settings")
		return
	}
	live, err := cfg.Hasher.LiveToolHashes(r.Context(), proof, conn.ID, conn.URL)
	if err != nil {
		// The error can name the upstream address: log the fact, not the text.
		log.Printf("tool changes: live check for %s failed", c.ConnectionID)
		writeError(w, http.StatusBadGateway, "could not check the tool with the MCP server: try again shortly")
		return
	}
	if live[c.Tool] != c.NewHash {
		writeError(w, http.StatusConflict, "the tool changed again since this change was recorded; review the newer change")
		return
	}
	// One transaction: the row, the grant entry and the event.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), proposalWriteBudget)
	defer cancel()
	tx, err := h.p.api.dbPool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update the tool change")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned
	done, err := db.AcceptToolChange(ctx, tx, who.Account, c.ID, who.Account)
	if errors.Is(err, db.ErrToolChangeState) {
		// A concurrent accept may have just closed the row: that is the idempotent answer.
		if again, gerr := db.GetOwnedToolChange(ctx, h.p.api.dbPool, who.Account, c.ID); gerr == nil && again.State == db.ToolChangeAccepted {
			writeJSON(w, http.StatusOK, viewOfToolChange(again))
			return
		}
	}
	if err != nil {
		transitionToolChangeError(w, err)
		return
	}
	var wrote bool
	action := db.PinRepinned
	// The mode the grant holds once this accept is done, for the event.
	effectiveMode := body.Mode
	switch c.Kind {
	case db.ToolChangeChanged:
		mode := ""
		if body.Mode == mcpgw.ModePropose {
			mode = mcpgw.ModePropose
		}
		wrote, err = db.RepinGrantTool(ctx, tx, c.SessionID, c.ConnectionID, c.Tool, c.OldHash, c.NewHash, mode)
		effectiveMode = c.Mode
		if mode != "" {
			effectiveMode = mode
		}
	case db.ToolChangeNew:
		action = db.PinGranted
		wrote, err = db.GrantTool(ctx, tx, c.SessionID, c.ConnectionID, c.Tool, c.NewHash, body.Mode)
	}
	if err != nil {
		log.Printf("tool changes: grant write: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update the session's grant")
		return
	}
	if !wrote {
		// Either the grant already pins new_hash (an earlier accept, or a resume's replace that
		// carried it: idempotent, proceed, but still honour a tighter mode) or the pin moved
		// elsewhere (409).
		if cur, err := db.ListMCPGrantsForSession(ctx, h.p.api.dbPool, c.SessionID); err == nil {
			for _, g := range cur {
				if g.ConnectionID != c.ConnectionID || g.Tools[c.Tool].Hash != c.NewHash {
					continue
				}
				entryMode := g.Tools[c.Tool].Mode
				if body.Mode == mcpgw.ModePropose && entryMode == mcpgw.ModeAllow {
					// Same hash in and out: the compare-and-set only tightens the mode.
					wrote, err = db.RepinGrantTool(ctx, tx, c.SessionID, c.ConnectionID, c.Tool, c.NewHash, c.NewHash, mcpgw.ModePropose)
					if err != nil {
						log.Printf("tool changes: grant tighten: %v", err)
						writeError(w, http.StatusInternalServerError, "could not update the session's grant")
						return
					}
					effectiveMode = mcpgw.ModePropose
				} else {
					wrote = true
					effectiveMode = entryMode
				}
			}
		}
	}
	if !wrote {
		writeError(w, http.StatusConflict, "the session's grant moved on since this change was recorded; the next listing re-evaluates it")
		return
	}
	if err := db.InsertPinEvent(ctx, tx, db.PinEvent{AccountID: who.Account, SessionID: c.SessionID, ConnectionID: c.ConnectionID, ConnectionName: c.ConnectionName,
		Tool: c.Tool, Action: action, OldHash: c.OldHash, NewHash: c.NewHash, Mode: effectiveMode, Actor: who.Account}); err != nil {
		log.Printf("tool changes: accept event: %v", err)
		writeError(w, http.StatusInternalServerError, "could not update the tool change")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update the tool change")
		return
	}
	log.Printf("tool changes: accepted id=%s account=%s connection=%s tool=%q mode=%s", c.ID, c.AccountID, c.ConnectionID, c.Tool, body.Mode)
	h.p.api.hub.RefreshSessionTools(c.SessionID)
	writeJSON(w, http.StatusOK, viewOfToolChange(done))
}

type pinEventView struct {
	ID             int64     `json:"id"`
	SessionID      string    `json:"session_id"`
	ConnectionID   string    `json:"connection_id"`
	ConnectionName string    `json:"connection_name"`
	Tool           string    `json:"tool"`
	Action         string    `json:"action"`
	OldHash        string    `json:"old_hash"`
	NewHash        string    `json:"new_hash"`
	Mode           string    `json:"mode"`
	Actor          string    `json:"actor"`
	CreatedAt      time.Time `json:"created_at"`
}

func writePinEvents(w http.ResponseWriter, evs []db.PinEvent, err error) {
	if err != nil {
		log.Printf("pin events: list: %v", err)
		writeError(w, http.StatusInternalServerError, "could not list pin events")
		return
	}
	out := make([]pinEventView, 0, len(evs))
	for _, e := range evs {
		out = append(out, pinEventView{ID: e.ID, SessionID: e.SessionID, ConnectionID: e.ConnectionID, ConnectionName: e.ConnectionName,
			Tool: e.Tool, Action: e.Action, OldHash: e.OldHash, NewHash: e.NewHash, Mode: e.Mode, Actor: e.Actor, CreatedAt: e.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// sessionEvents answers GET /api/sessions/{id}/pin-events: the caller's own events for the
// session (account_id = caller on the query; a foreign or unknown session lists nothing).
func (h *toolChangesHandler) sessionEvents(w http.ResponseWriter, r *http.Request) {
	who, ok := h.p.person(w, r)
	if !ok {
		return
	}
	evs, err := db.ListPinEventsForSession(r.Context(), h.p.api.dbPool, who.Account, r.PathValue("id"), 200)
	writePinEvents(w, evs, err)
}

// connectionEvents answers GET /api/mcp/connections/{id}/pin-events, scoped the same way.
func (h *toolChangesHandler) connectionEvents(w http.ResponseWriter, r *http.Request) {
	who, ok := h.p.person(w, r)
	if !ok {
		return
	}
	evs, err := db.ListPinEventsForConnection(r.Context(), h.p.api.dbPool, who.Account, r.PathValue("id"), 200)
	writePinEvents(w, evs, err)
}
