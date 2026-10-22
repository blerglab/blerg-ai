package server

// Session permissions for proposals (0.4.3). The owner can let one session's proposals of one
// narrow scope be approved without asking: board cards on ONE board (built-in filing), or ONE
// MCP write tool of ONE connection. A permission is created from the chat's proposal card ("Allow
// for this session"), the session panel, or the launch sheet; it is the owner's alone to create or
// revoke (a signed-in person, never an agent or session token), and it ends with the session or a
// discard of its parent.
//
// An automatic approval is not a second approval path. It calls proposalsHandler.execute, the
// person's own approval, with the permission's creator as the approver (their account and the
// login session they granted it with): the same snapshot checks, the same compare-and-set, the
// same execution and the same recording. The compare-and-set also requires the permission to be
// live, and records it on the proposal (approved_by_grant), so every automatic approval names the
// permission that allowed it.
//
// The chat learns of proposals through two session events that carry no draft and no arguments:
// proposal_request (id and kind) and proposal_decided (the outcome; for a filed card, its board,
// number, title, type, priority and column, which the History tab shows as a chip). The card
// itself reads the proposal through the owner-only proposals API.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Messages a refused permission answers with.
const (
	msgPermNotActive   = "This session is not active, so it cannot be given permissions."
	msgPermCron        = "A cron session's proposals always wait for you; session permissions are not available for crons."
	msgPermRestricted  = "A restricted session's proposals always wait for you; session permissions are not available for it."
	msgPermBoardRun    = "A board session's proposals always wait for you; session permissions are not available for it."
	msgPermStarter     = "Only sessions you started, and the sessions they start, can have session permissions."
	msgPermKind        = "Only agent sessions can have session permissions."
	msgPermSignIn      = "Session permissions need your current sign-in: sign in again and retry."
	msgPermPlugin      = "A plugin request is always decided by you; it cannot be allowed for the session."
	msgPermBoardCreate = "A proposed board is always decided by you; it cannot be allowed for the session."
	msgPermMCPTool     = "That tool is not one this session must ask about (it is not in approval mode for this session)."
	msgPermBuiltin     = "Built-in connections cannot be allowed for the session."
)

// proposalGrantProblem is why the session cannot hold a proposal permission, or "". It does not
// look at who asks (permissionOwner does) nor at the scope.
func proposalGrantProblem(row *db.SessionRow) string {
	switch {
	case row == nil:
		return msgPermNotActive
	case row.Kind != "agent":
		return msgPermKind
	case row.DiscardRequestedAt != nil || row.ArchivedAt != nil || db.TerminallyEnded(row) || terminalSessionStatus(row.Status):
		return msgPermNotActive
	case derefOrEmpty(row.CronID) != "" || row.StartedByKind == "cron":
		return msgPermCron
	case row.RestrictTools:
		return msgPermRestricted
	case derefOrEmpty(row.BoardID) != "":
		return msgPermBoardRun
	case row.StartedByKind != "" && row.StartedByKind != "human" && row.StartedByKind != "session":
		return msgPermStarter
	}
	return ""
}

// permissionOwner authenticates a signed-in person with a live login session: the only caller that
// may read, create or revoke session permissions. An agent token, a session token, a delegated
// principal and the runner key are refused.
func (a *API) permissionOwner(w http.ResponseWriter, r *http.Request) (grantRequester, bool) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return grantRequester{}, false
	}
	if p.Kind != string(identity.Human) || p.OnBehalfOf != "" || p.Sub == "" {
		writeError(w, http.StatusForbidden, "forbidden")
		return grantRequester{}, false
	}
	if p.Sid == "" {
		writeError(w, http.StatusUnauthorized, msgPermSignIn)
		return grantRequester{}, false
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return grantRequester{}, false
	}
	return requesterOf(p), true
}

// ownedSession reads the caller's session for a permissions route; another account's, a missing
// one and a malformed id all answer the same 404.
func (a *API) ownedSession(ctx context.Context, w http.ResponseWriter, account, sid string) (*db.SessionRow, bool) {
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "read the session", "permissions GetSession", err)
		return nil, false
	}
	if row == nil || !ownsSession(account, row) {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return row, true
}

// proposalGrantView is one permission as the owner's browser sees it. The login session it acts
// with is deliberately absent.
type proposalGrantView struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Level is a board permission's level (propose, read, file, full); "" for an MCP tool.
	Level               string `json:"level,omitempty"`
	BoardID             string `json:"board_id,omitempty"`
	BoardName           string `json:"board_name,omitempty"`
	ConnectionID        string `json:"connection_id,omitempty"`
	ConnectionName      string `json:"connection_name,omitempty"`
	Tool                string `json:"tool,omitempty"`
	CreatedVia          string `json:"created_via"`
	CreatedFromProposal string `json:"created_from_proposal,omitempty"`
	CreatedAt           string `json:"created_at"`
	// Active is whether it approves now. Inactive says why not: "signed_out" (the sign-in it was
	// given from has ended; give it again) or "tool_changed" (the tool's definition changed since).
	Active   bool   `json:"active"`
	Inactive string `json:"inactive,omitempty"`
}

func viewOfProposalGrant(g *db.ProposalGrant) proposalGrantView {
	level := ""
	if g.Kind == db.ProposalGrantBoard {
		level = g.Level
	}
	return proposalGrantView{ID: g.ID, Kind: g.Kind, Level: level, BoardID: g.BoardID, BoardName: g.BoardName, ConnectionID: g.ConnectionID,
		ConnectionName: g.ConnectionName, Tool: g.Tool, CreatedVia: g.CreatedVia, CreatedFromProposal: g.CreatedFromProposal,
		CreatedAt: g.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"), Active: true}
}

// pinnedToolHashes maps "<connection>\x00<tool>" to the definition the session has pinned now.
func (a *API) pinnedToolHashes(ctx context.Context, sessionID string) (map[string]string, error) {
	grants, err := db.ListMCPGrantsForSession(ctx, a.dbPool, sessionID)
	out := map[string]string{}
	for _, g := range grants {
		for name, t := range g.Tools {
			out[g.ConnectionID+"\x00"+name] = t.Hash
		}
	}
	return out, err
}

// signInAlive reports whether a grant's sign-in can still act: core answers a refused proof for a
// login session that ended. Unknown (core unreachable or not configured) counts as alive: the
// approval itself still checks it. seen caches answers for one request.
func (a *API) signInAlive(ctx context.Context, seen map[string]bool, account, sid, cacheScope string) bool {
	if v, ok := seen[sid]; ok {
		return v
	}
	alive := a.permissionSignIns.load(ctx, cacheScope+"\x00"+account+"\x00"+sid, func(ctx context.Context) (bool, bool) {
		if cfg := a.hub.mcpStart.get(); cfg != nil && cfg.Core != nil {
			_, err := cfg.Core.ListConnections(ctx, mcpgw.Proof{AccountID: account, SessionID: sid})
			if errors.Is(err, ErrMCPProofInvalid) {
				return false, true
			}
			return true, err == nil
		}
		return true, false
	})
	seen[sid] = alive
	return alive
}

// mcpToolOption is a propose-mode tool of the session: one the panel can allow.
type mcpToolOption struct {
	ConnectionID   string `json:"connection_id"`
	ConnectionName string `json:"connection_name"`
	Tool           string `json:"tool"`
}

// proposeTools lists the session's propose-mode tools of the person's own connections (never a
// built-in), sorted by connection name and tool.
func (a *API) proposeTools(ctx context.Context, sessionID string) ([]mcpToolOption, error) {
	grants, err := db.ListMCPGrantsForSession(ctx, a.dbPool, sessionID)
	if err != nil {
		return nil, err
	}
	out := []mcpToolOption{}
	for _, g := range grants {
		if g.Builtin != "" {
			continue
		}
		for name, t := range g.Tools {
			if t.Mode == mcpgw.ModePropose {
				out = append(out, mcpToolOption{ConnectionID: g.ConnectionID, ConnectionName: g.Name, Tool: name})
			}
		}
	}
	slices.SortFunc(out, func(x, y mcpToolOption) int {
		if c := strings.Compare(x.ConnectionName, y.ConnectionName); c != 0 {
			return c
		}
		return strings.Compare(x.Tool, y.Tool)
	})
	return out, nil
}

// HandleGetSessionPermissions is GET /api/sessions/{id}/permissions: the live proposal
// permissions, whether the session can hold them, and the MCP tools it could allow. Boards come
// from GET /api/session-boards. Child approvals stay on .../child-approval.
func (a *API) HandleGetSessionPermissions(w http.ResponseWriter, r *http.Request) {
	who, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	row, ok := a.ownedSession(r.Context(), w, who.Account, r.PathValue("id"))
	if !ok {
		return
	}
	grants, err := db.ListLiveProposalGrants(r.Context(), a.dbPool, row.ID)
	if err != nil {
		writeDatabaseFailure(w, "read the session permissions", "list proposal grants", err)
		return
	}
	tools, err := a.proposeTools(r.Context(), row.ID)
	if err != nil {
		writeDatabaseFailure(w, "read the session permissions", "list propose tools", err)
		return
	}
	// The database snapshot is part of the cache key: changes invalidate across
	// replicas without sharing a process-local invalidation signal.
	rawGrants, _ := json.Marshal(grants)
	cacheScope := row.ID + fmt.Sprintf("%x", sha256.Sum256(rawGrants))
	views := make([]proposalGrantView, 0, len(grants))
	pinned, _ := a.pinnedToolHashes(r.Context(), row.ID)
	alive := map[string]bool{who.Sid: true}
	for i := range grants {
		v := viewOfProposalGrant(&grants[i])
		switch {
		case grants[i].Kind == db.ProposalGrantMCP && pinned[grants[i].ConnectionID+"\x00"+grants[i].Tool] != grants[i].ToolHash:
			v.Active, v.Inactive = false, "tool_changed"
		case !a.signInAlive(r.Context(), alive, who.Account, grants[i].ProofSessionID, cacheScope):
			v.Active, v.Inactive = false, "signed_out"
		}
		views = append(views, v)
	}
	boards, err := a.boardLevelsFor(r.Context(), row)
	if err != nil {
		writeDatabaseFailure(w, "read the session permissions", "board levels", err)
		return
	}
	for i := range boards {
		if boards[i].child {
			// A started session's board acts with its parent's sign-in for it: that is the one
			// that has to be alive (the child's own row has none of its own).
			if p := boards[i].capProof; boards[i].Level != db.BoardLevelPropose && p.SessionID != "" && !a.signInAlive(r.Context(), alive, p.AccountID, p.SessionID, cacheScope) {
				boards[i].Active, boards[i].Inactive = false, "signed_out"
			}
			continue
		}
		// An untouched launch board acts with the launch's own sign-in.
		if p := boards[i].launchProof; boards[i].GrantID == "" && p.SessionID != "" && !a.signInAlive(r.Context(), alive, p.AccountID, p.SessionID, cacheScope) {
			boards[i].Active, boards[i].Inactive = false, "signed_out"
		}
		for _, v := range views {
			if v.ID == boards[i].GrantID && v.Kind == db.ProposalGrantBoard && !v.Active {
				boards[i].Active, boards[i].Inactive = false, v.Inactive
			}
		}
	}
	reason := proposalGrantProblem(row)
	w.Header().Set("Cache-Control", "no-store")
	// boards: every board above propose (the Boards section and its chips), the launch board
	// included; grants: every live permission row (board rows carry their level). For a session
	// started by another one, started_by names that session: its levels cap this one's (each board
	// has own_level and parent_level, and level is the lower), and parent_boards are its levels now,
	// the most this session can be given.
	out := map[string]any{"can_grant": reason == "", "reason": reason, "grants": views, "mcp_tools": tools, "boards": boards}
	if isChildSession(row) {
		started := map[string]any{"id": derefOrEmpty(row.ParentSessionID), "title": ""}
		parentBoards := []map[string]string{}
		if parent, err := a.childBoardParent(r.Context(), row); err == nil && parent != nil {
			started["title"] = sessionLabel(derefOrEmpty(parent.Title), parent.Repo, parent.ID)
			if levels, err := a.sessionBoardLevels(r.Context(), parent.ID); err == nil {
				for _, b := range levels {
					parentBoards = append(parentBoards, map[string]string{"board_id": b.BoardID, "board_name": b.BoardName, "level": b.Level})
				}
			}
		}
		started["boards"] = parentBoards
		out["started_by"] = started
	}
	writeJSON(w, http.StatusOK, out)
}

// proposalGrantRequest is the body of POST /api/sessions/{id}/proposal-grants: exactly one scope.
type proposalGrantRequest struct {
	Kind         string `json:"kind"`
	BoardID      string `json:"board_id,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	Tool         string `json:"tool,omitempty"`
}

// HandlePostProposalGrant is POST /api/sessions/{id}/proposal-grants: the owner allows one scope.
func (a *API) HandlePostProposalGrant(w http.ResponseWriter, r *http.Request) {
	who, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	row, ok := a.ownedSession(r.Context(), w, who.Account, r.PathValue("id"))
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	var req proposalGrantRequest
	if err != nil || filingDecode(raw, &req) != nil {
		writeError(w, http.StatusBadRequest, `the body is {"kind":"board_file","board_id":…} or {"kind":"mcp_tool","connection_id":…,"tool":…}`)
		return
	}
	g, apiErr := a.createProposalGrant(r.Context(), who, row, req, "panel", "")
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, viewOfProposalGrant(g))
}

// HandleRevokeProposalGrant is POST /api/proposal-grants/{id}/revoke: owner only; another
// account's grant answers 404. Revoking twice is not an error.
func (a *API) HandleRevokeProposalGrant(w http.ResponseWriter, r *http.Request) {
	who, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	g, err := db.GetOwnedProposalGrant(r.Context(), a.dbPool, r.PathValue("id"), who.Account)
	if err != nil {
		writeDatabaseFailure(w, "revoke the permission", "get proposal grant", err)
		return
	}
	if g == nil || (g.Kind == db.ProposalGrantBoard && who.Project != "" && g.BoardID != who.Project) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	changed, err := db.RevokeProposalGrant(r.Context(), a.dbPool, g.ID, who.Account, "revoked")
	if err != nil {
		writeDatabaseFailure(w, "revoke the permission", "revoke proposal grant", err)
		return
	}
	if changed && g.Kind == db.ProposalGrantBoard && g.Level != db.BoardLevelPropose {
		if row, err := db.GetSession(r.Context(), a.dbPool, g.SessionID); err == nil && row != nil {
			name := boardDisplayName(g.BoardName, g.BoardID)
			from := g.Level
			if isChildSession(row) {
				// What the child had: its own level under its parent's cap.
				if parent, err := a.childBoardParent(r.Context(), row); err == nil {
					if lv, err := a.applyParentCap(r.Context(), boardLevelOf{Level: g.Level}, parent, g.BoardID); err == nil {
						from = lv.Level
					}
				}
			}
			if from != db.BoardLevelPropose {
				a.relayBoardAccess(r.Context(), who.Account, row, g.ID+"-revoked", name, from, db.BoardLevelPropose)
			}
			a.relayChildrenBoardAccess(r.Context(), who.Account, row, "board-access-"+g.ID+"-revoked", g.BoardID, name, g.Level, db.BoardLevelPropose)
		}
	}
	log.Printf("session permissions: revoked grant=%s session=%s account=%s kind=%s", g.ID, g.SessionID, g.AccountID, g.Kind)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "id": g.ID})
}

// createProposalGrant checks the session and the scope, then stores the permission. A board must
// be one the person can file to now (their discovery, by id); an MCP tool must be in propose mode
// in this session's own grant of that connection. via is card | panel | launch.
func (a *API) createProposalGrant(ctx context.Context, who grantRequester, row *db.SessionRow, req proposalGrantRequest, via, fromProposal string) (*db.ProposalGrant, *APIError) {
	if who.Kind != string(identity.Human) || who.Account == "" || who.Sid == "" {
		return nil, apiErrorf(http.StatusUnauthorized, "%s", msgPermSignIn)
	}
	if row == nil || !ownsSession(who.Account, row) {
		return nil, apiErrorf(http.StatusNotFound, "not found")
	}
	if problem := proposalGrantProblem(row); problem != "" {
		return nil, apiErrorf(http.StatusConflict, "%s", problem)
	}
	g := db.ProposalGrant{SessionID: row.ID, AccountID: who.Account, Kind: req.Kind, ProofSessionID: who.Sid,
		ProjectScope: who.Project, CreatedVia: via, CreatedFromProposal: fromProposal}
	switch req.Kind {
	case db.ProposalGrantBoard:
		if req.BoardID == "" || req.ConnectionID != "" || req.Tool != "" || !filingText(req.BoardID, 200, false) {
			return nil, apiErrorf(http.StatusBadRequest, "a board permission names one board_id and nothing else")
		}
		// Filing without asking is board level file (0.4.4): it raises the board to file and never
		// lowers a board that is already full.
		level := db.BoardLevelFile
		if now, err := a.effectiveBoardLevel(ctx, row, req.BoardID); err != nil {
			return nil, apiErrorf(http.StatusServiceUnavailable, "could not read the session's board access; retry")
		} else if db.BoardLevelRank(now.Own) > db.BoardLevelRank(level) {
			level = now.Own
		}
		return a.setBoardLevel(ctx, who, row, req.BoardID, level, via, fromProposal)
	case db.ProposalGrantMCP:
		if req.ConnectionID == "" || req.Tool == "" || req.BoardID != "" || !utf8.ValidString(req.Tool) || len(req.Tool) > 256 {
			return nil, apiErrorf(http.StatusBadRequest, "an MCP permission names one connection_id and one tool and nothing else")
		}
		grants, err := db.ListMCPGrantsForSession(ctx, a.dbPool, row.ID)
		if err != nil {
			return nil, apiErrorf(http.StatusServiceUnavailable, "could not read the session's connections; retry")
		}
		found := false
		for _, mg := range grants {
			if mg.ConnectionID != req.ConnectionID {
				continue
			}
			if mg.Builtin != "" {
				return nil, apiErrorf(http.StatusUnprocessableEntity, "%s", msgPermBuiltin)
			}
			if t, ok := mg.Tools[req.Tool]; ok && t.Mode == mcpgw.ModePropose && t.Hash != "" {
				found = true
				// Bound to the definition pinned now: a changed definition asks again.
				g.ConnectionID, g.ConnectionName, g.Tool, g.ToolHash = mg.ConnectionID, mg.Name, req.Tool, t.Hash
			}
		}
		if !found {
			return nil, apiErrorf(http.StatusUnprocessableEntity, "%s", msgPermMCPTool)
		}
	default:
		return nil, apiErrorf(http.StatusBadRequest, `kind is "board_file" or "mcp_tool"`)
	}
	out, err := db.InsertProposalGrant(ctx, a.dbPool, g)
	if errors.Is(err, db.ErrProposalGrantSession) {
		return nil, apiErrorf(http.StatusConflict, "%s", msgPermNotActive)
	}
	if err != nil {
		log.Printf("session permissions: insert: %v", err)
		return nil, apiErrorf(http.StatusInternalServerError, "could not save the permission")
	}
	log.Printf("session permissions: granted grant=%s session=%s account=%s kind=%s via=%s", out.ID, out.SessionID, out.AccountID, out.Kind, via)
	return out, nil
}

// grantFromProposal is "Allow for this session" on a proposal: a permission of exactly the
// proposal's scope (its board, or its connection and tool), created by the approving person.
func (a *API) grantFromProposal(ctx context.Context, who grantRequester, p *db.Proposal) (*db.ProposalGrant, *APIError) {
	if db.IsSessionPluginProposal(p) {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "%s", msgPermPlugin)
	}
	if db.IsBoardCreateProposal(p) {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "%s", msgPermBoardCreate)
	}
	if p.SessionID == "" {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "%s", msgPermCron)
	}
	row, err := db.GetSession(ctx, a.dbPool, p.SessionID)
	if err != nil {
		return nil, apiErrorf(http.StatusServiceUnavailable, "could not read the session; retry")
	}
	req := proposalGrantRequest{Kind: db.ProposalGrantMCP, ConnectionID: p.ConnectionID, Tool: p.Tool}
	if db.IsBoardFilingProposal(p) {
		var args filingArguments
		if json.Unmarshal(p.Arguments, &args) != nil || args.BoardID == "" {
			return nil, apiErrorf(http.StatusConflict, "The saved draft is invalid.")
		}
		req = proposalGrantRequest{Kind: db.ProposalGrantBoard, BoardID: args.BoardID}
	}
	return a.createProposalGrant(ctx, who, row, req, "card", p.ID)
}

// proposalGrantScope is the scope a proposal falls under: kind, board, connection, tool and, for
// an MCP call, the tool's pinned definition (hash) it was made with.
func proposalGrantScope(p *db.Proposal) (kind, board, connection, tool, hash string, ok bool) {
	switch {
	case db.IsSessionPluginProposal(p), db.IsBoardCreateProposal(p):
		return "", "", "", "", "", false
	case db.IsBoardFilingProposal(p):
		var args filingArguments
		if json.Unmarshal(p.Arguments, &args) != nil || args.BoardID == "" {
			return "", "", "", "", "", false
		}
		return db.ProposalGrantBoard, args.BoardID, "", "", "", true
	}
	return db.ProposalGrantMCP, "", p.ConnectionID, p.Tool, p.ToolHash, true
}

// defaultGrantHourlyCap is how many proposals one session permission may approve on its own in an
// hour (BLERG_RUNNER_GRANT_HOURLY_CAP overrides it). Past it, proposals ask the person again.
const defaultGrantHourlyCap = 20

func (a *API) grantHourlyCap() int {
	if a.grantCapOverride > 0 {
		return a.grantCapOverride
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("BLERG_RUNNER_GRANT_HOURLY_CAP"))); err == nil && n > 0 {
		return n
	}
	return defaultGrantHourlyCap
}

// grantFor is the live session permission that covers exactly the proposal's scope, or nil.
func (a *API) grantFor(ctx context.Context, p *db.Proposal) *db.ProposalGrant {
	if a.dbPool == nil || p == nil || p.SessionID == "" || p.State != db.ProposalPending {
		return nil
	}
	kind, board, connection, tool, hash, ok := proposalGrantScope(p)
	if !ok {
		return nil
	}
	g, err := db.LiveProposalGrantFor(ctx, a.dbPool, p.SessionID, p.AccountID, kind, board, connection, tool, hash)
	if err != nil {
		log.Printf("session permissions: find grant for proposal %s: %v", p.ID, err)
		return nil
	}
	if g != nil && kind == db.ProposalGrantBoard {
		return a.childFilingGrant(ctx, g)
	}
	return g
}

// settleNewProposal shows a just-queued proposal in its session's chat and, when a live session
// permission covers exactly its scope and has not reached its hourly cap, approves it through
// proposalsHandler.execute. It returns the proposal as it is now (nil when no permission approved
// it: it stays pending, for the person) and, when the cap held it back, the sentence that says so
// (on the card and to the agent). A refusal (the board is no longer the creator's, the connection
// changed, the sign-in ended) leaves it pending too.
func (a *API) settleNewProposal(ctx context.Context, p *db.Proposal) (*db.Proposal, string) {
	g := a.grantFor(ctx, p)
	note := ""
	if g != nil {
		limit := a.grantHourlyCap()
		n, err := db.CountGrantApprovalsSince(ctx, a.dbPool, g.ID, time.Now().Add(-time.Hour))
		switch {
		case err != nil:
			log.Printf("session permissions: count approvals of grant %s: %v", g.ID, err)
			g, note = nil, "The session permission could not be checked, so this one waits for you."
		case n >= limit:
			log.Printf("session permissions: grant=%s reached its hourly cap (%d); proposal=%s waits for the person", g.ID, limit, p.ID)
			g, note = nil, fmt.Sprintf("The session permission already approved %d in the last hour (its limit), so this one waits for you.", n)
		}
	}
	a.announceProposalRequest(ctx, p, note)
	if g == nil {
		return nil, note
	}
	who := grantRequester{Kind: string(identity.Human), Account: g.AccountID, Sid: g.ProofSessionID, Project: g.ProjectScope}
	capture := &filingToolCapture{header: make(http.Header)}
	if a.beforeGrantApproval != nil {
		a.beforeGrantApproval()
	}
	(&proposalsHandler{api: a}).execute(ctx, capture, who, p, string(p.Arguments), g.ID)
	now, err := db.GetOwnedProposal(context.WithoutCancel(ctx), a.dbPool, p.AccountID, p.ID)
	if err != nil {
		log.Printf("session permissions: reread proposal %s: %v", p.ID, err)
		return nil, ""
	}
	if now.ApprovedByGrant == g.ID {
		log.Printf("session permissions: auto-approved proposal=%s grant=%s session=%s state=%s", p.ID, g.ID, p.SessionID, now.State)
	} else if capture.status >= 400 {
		log.Printf("session permissions: grant=%s did not approve proposal=%s (status %d); it waits for the person", g.ID, p.ID, capture.status)
		// A burst can pass the count above and meet the grant's atomic count instead: say so.
		if now.State == db.ProposalPending {
			limit := a.grantHourlyCap()
			if n, err := db.CountGrantApprovalsSince(context.WithoutCancel(ctx), a.dbPool, g.ID, time.Now().Add(-time.Hour)); err == nil && n >= limit {
				note = fmt.Sprintf("The session permission already approved %d in the last hour (its limit), so this one waits for you.", n)
			}
		}
	}
	a.announceProposalDecision(context.WithoutCancel(ctx), p.AccountID, p.ID)
	return now, note
}

// ---- session events ------------------------------------------------------------------------

// proposalEventKind is the card kind: board_file, or mcp for a connection's tool.
func proposalEventKind(p *db.Proposal) string {
	if db.IsBoardFilingProposal(p) {
		return "board_file"
	}
	if db.IsBoardCreateProposal(p) {
		return "board_create"
	}
	return "mcp"
}

// announceProposalRequest puts the proposal's card in its session's chat. The event names the
// proposal only: the card reads the rest through the owner-only API.
// note, when set, says why it asks although a session permission covers it (its hourly cap).
func (a *API) announceProposalRequest(ctx context.Context, p *db.Proposal, note string) {
	if p == nil || p.SessionID == "" || db.IsSessionPluginProposal(p) {
		return
	}
	payload := map[string]string{"proposal_id": p.ID, "kind": proposalEventKind(p)}
	if note != "" {
		payload["note"] = note
	}
	recordServerAgentEvent(ctx, a.hub, a.dbPool, p.SessionID, protocol.SessionMessageEventID("proposal:"+p.ID, "proposal_request"),
		"proposal_request", payload)
}

// filedCard is a created board card as the chat and the History tab show it. It carries no body.
type filedCard struct {
	ID        string `json:"id"`
	BoardID   string `json:"board_id"`
	BoardName string `json:"board_name,omitempty"`
	Number    int    `json:"number"`
	Title     string `json:"title,omitempty"`
	Type      string `json:"type,omitempty"`
	Priority  string `json:"priority,omitempty"`
	Column    string `json:"column,omitempty"`
}

// announceProposalDecision records a decided proposal in its session's chat (once per outcome):
// its state, whether a permission approved it (and which), and for a filed card the card.
func (a *API) announceProposalDecision(ctx context.Context, account, id string) {
	if a.dbPool == nil {
		return
	}
	p, err := db.GetOwnedProposal(ctx, a.dbPool, account, id)
	if err != nil || p.SessionID == "" || db.IsSessionPluginProposal(p) || p.State == db.ProposalPending || p.State == db.ProposalExecuting {
		return
	}
	payload := map[string]any{"proposal_id": p.ID, "kind": proposalEventKind(p), "state": p.State, "auto": p.ApprovedByGrant != ""}
	if p.ApprovedByGrant != "" {
		payload["grant_id"] = p.ApprovedByGrant
	}
	if db.IsBoardFilingProposal(p) && p.State == db.ProposalDone {
		var card filedCard
		if json.Unmarshal(p.Result, &card) == nil && card.ID != "" && card.Number > 0 {
			payload["card"] = card
		}
	}
	if b := readCreatedBoard(p); b != nil {
		payload["board"] = map[string]string{"board_id": b.ID, "board_name": b.Name, "access": b.Access}
	}
	inserted, err := appendServerAgentEventOnce(ctx, a.hub, a.dbPool, p.SessionID, protocol.SessionMessageEventID("proposal:"+p.ID, "proposal_decided:"+p.State),
		"proposal_decided", payload)
	if err != nil {
		log.Printf("agent event %s/proposal_decided: %v", p.SessionID, err)
		return
	}
	// Each outcome is told to the agent once, with the event that records it.
	if inserted {
		a.deliverProposalDecision(ctx, p)
	}
}

// boardCardDescriber reads a created card's public summary (title, type, priority, column) with
// the approver's read access. Optional: without it the summary comes from the draft.
type boardCardDescriber interface {
	Describe(ctx context.Context, proof mcpgw.Proof, board, cardID string) (filedCard, error)
}

// filedCardResult is the stored result of a filing that created a card: the receipt (id, board,
// number) plus the board's name and the card's title, type, priority and column. Reading the card
// back is best effort; the draft's title and type stand in when it fails.
func (a *API) filedCardResult(ctx context.Context, up boardFilingUpstream, proof mcpgw.Proof, board SessionBoard, args filingArguments, receipt json.RawMessage) json.RawMessage {
	var card filedCard
	if json.Unmarshal(receipt, &card) != nil || card.ID == "" {
		return receipt
	}
	if card.BoardID == "" {
		card.BoardID = args.BoardID
	}
	card.BoardName, card.Title, card.Type = board.Name, args.Title, args.Type
	if d, ok := up.(boardCardDescriber); ok {
		if got, err := d.Describe(ctx, proof, card.BoardID, card.ID); err == nil {
			if got.Title != "" {
				card.Title = got.Title
			}
			if got.Type != "" {
				card.Type = got.Type
			}
			card.Priority, card.Column = got.Priority, got.Column
		}
	}
	out, err := json.Marshal(card)
	if err != nil {
		return receipt
	}
	return out
}

// ---- the MCP gateway's proposals -----------------------------------------------------------

// sessionGrantSink is the gateway's proposal sink with session permissions: it queues the call
// exactly as before, shows the proposal in the session's chat, and, when a live permission covers
// this connection and tool in this session, approves it through the person's approval path and
// tells the agent the outcome instead of "queued".
type sessionGrantSink struct {
	inner *mcpgw.DBProposalSink
	api   *API
}

func (s *sessionGrantSink) Propose(ctx context.Context, req mcpgw.ProposalRequest) (string, error) {
	p, text, err := s.inner.ProposeRow(ctx, req)
	if err != nil || p == nil {
		return text, err
	}
	now, note := s.api.settleNewProposal(ctx, p)
	if now == nil || now.ApprovedByGrant == "" {
		if note != "" {
			text += " " + note
		}
		return text, nil
	}
	switch now.State {
	case db.ProposalDone:
		return "This action was approved by a session permission the person gave and it ran. Proposal id: " + p.ID +
			". Result: " + resultText(now.Result), nil
	case db.ProposalFailed:
		return "This action was approved by a session permission the person gave, but it failed. Proposal id: " + p.ID +
			". Result: " + resultText(now.Result), nil
	default:
		return "This action was approved by a session permission the person gave, but its outcome is unknown: it may or may not have happened. " +
			"Do not repeat it. Proposal id: " + p.ID + ".", nil
	}
}

// resultText is a tool result's text parts (or its error), capped, for the agent.
func resultText(raw json.RawMessage) string {
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "(no readable result)"
	}
	if r.Error != "" {
		return r.Error
	}
	parts := []string{}
	for _, c := range r.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	out := strings.Join(parts, "\n")
	if utf8.RuneCountInString(out) > 4000 {
		out = string([]rune(out)[:4000]) + "…"
	}
	if out == "" {
		return "(no text)"
	}
	return out
}

// installSessionGrantSink wraps the gateway's database sink once.
func (a *API) installSessionGrantSink(gw *mcpgw.Gateway) {
	if gw == nil {
		return
	}
	if inner, ok := gw.ProposalSink().(*mcpgw.DBProposalSink); ok {
		gw.SetProposalSink(&sessionGrantSink{inner: inner, api: a})
	}
}

// RegisterSessionPermissionRoutes mounts the session permission routes (human owner only).
func (a *API) RegisterSessionPermissionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/permissions", a.HandleGetSessionPermissions)
	mux.HandleFunc("POST /api/sessions/{id}/proposal-grants", a.HandlePostProposalGrant)
	mux.HandleFunc("POST /api/proposal-grants/{id}/revoke", a.HandleRevokeProposalGrant)
}
