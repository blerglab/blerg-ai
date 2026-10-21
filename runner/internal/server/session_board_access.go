package server

// Board access levels (0.4.4). The Permissions modal's Boards section gives a session one level per
// board, and each level includes the ones below it:
//
//	propose  the default for every board, nothing stored: list boards, search for duplicates, and
//	         propose a card (blerg-runner ticket file / blerg_ticket_file) that asks the person;
//	read     also list, get and search every card;
//	file     also file proposals without asking (the 0.4.3 board filing permission);
//	full     also create, update, move, link and comment: what choosing the board at launch gives.
//
// Storage is the 0.4.3 session permission row (kind board_file) with a level (migration 070). A
// change never edits a row: the old one is revoked and a new one inserted (db.InsertProposalGrant),
// so who set which level when is kept. The level a board has now is its latest row in the session's
// current lifetime (db.LatestBoardGrant); with no row, the board a launch gave a connection to is
// full and every other board is propose.
//
// Every path that touches a board reads that level on every call, so lowering or revoking holds at
// the agent's next command or call:
//   - the session's own board tools (this file): POST /api/sessions/{id}/board-tools, used by the
//     blerg-runner board commands when the session has no launch-time board credential, and the
//     blerg_board tool of the session's built-in MCP server. Both authenticate with the session's
//     own token; the runner exchanges a board credential for that one call (card.read-only for a
//     read), uses it and revokes it. The agent never holds a board or core credential.
//   - the launch board's gateway connection (mcpgw.boardGrantLevel), which the engine's board MCP
//     tools and the launch-time CLI credential go through.
//   - filing without asking (db.LiveProposalGrantFor accepts only file and full).
//
// Setting a level is the owner's alone (permissionOwner: a signed-in person with a live login
// session, never an agent, session or delegated token), refused for restricted, cron and
// board-started sessions as every session permission is (proposalGrantProblem), and logged.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const (
	msgBoardToolsUnavailable = "Board access through this session is not available: the runner has no board connection configured."
	msgBoardToolsSession     = "This session cannot use board access from its Permissions; use the board it was started with, or propose a card (blerg-runner ticket file), which asks the person."
	maxBoardToolArgsBytes    = 64 << 10
	msgBoardCallsAtOnce      = "too many board calls at once from this session (at most 4 run together); wait for one to finish and retry"
	msgBoardCallsPerMinute   = "too many board requests from this session; retry in %d seconds"
)

// Limits of one session's board requests. Each call holds a core exchange token for its length, and
// core caps an account's live exchange tokens (100, shared with every board connection of the
// account), so the number in flight is bounded, not only the rate. Variables for the tests.
var (
	// Named CLI commands use several requests (a move uses seven). Leave room
	// for ordinary interactive work; the independent in-flight cap protects exchanges.
	boardCallsPerMinute = 240
	boardCallsInFlight  = 4
	boardCallWait       = 2 * time.Second
)

// sessionBoardCaller is the gateway's CallBoard (mcpgw.Gateway); tests substitute a fake.
type sessionBoardCaller interface {
	CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error)
}

// boardLevelOf is one board's level in a session now, and what it rests on.
type boardLevelOf struct {
	Level string
	State *db.BoardGrantState // the latest row of the current lifetime, or nil
	// Launch is the session's launch-time board connection to this board, or nil.
	Launch *db.MCPGrant
	// Own is the session's own level (for a child, before its parent's cap); Child marks a session
	// started by another one, whose Level is min(Own, Cap) with Cap its parent's level now
	// (child_board_access.go). A child's calls act with capProof, its parent's sign-in.
	Own        string
	Child      bool
	Cap        string
	capProof   mcpgw.Proof
	capProject string
}

// proof is the sign-in the session's board calls act with: the person who set the level (their
// login session; signing out stops it), or for an untouched launch board, the launch's own proof.
func (b boardLevelOf) proof() (mcpgw.Proof, bool) {
	if b.Child {
		return b.childBoardProof()
	}
	if b.State != nil && b.State.Live {
		p := mcpgw.Proof{AccountID: b.State.Grant.AccountID, SessionID: b.State.Grant.ProofSessionID}
		return p, p.Valid()
	}
	if b.State == nil && b.Launch != nil {
		p := mcpgw.ProofFromGrant(b.Launch.AccountID, b.Launch.ProofKind, b.Launch.ProofValue)
		return p, p.Valid()
	}
	return mcpgw.Proof{}, false
}

// launchBoardGrant is the session's built-in board connection (a board chosen at launch), or nil.
func (a *API) launchBoardGrant(ctx context.Context, sessionID string) (*db.MCPGrant, error) {
	grants, err := db.ListMCPGrantsForSession(ctx, a.dbPool, sessionID)
	if err != nil {
		return nil, err
	}
	for i := range grants {
		if grants[i].Builtin == mcpgw.BuiltinBoard && grants[i].BuiltinRef != "" {
			return &grants[i], nil
		}
	}
	return nil, nil
}

// sessionBoardLevel is the board's level in the session now.
func (a *API) sessionBoardLevel(ctx context.Context, sessionID, boardID string) (boardLevelOf, error) {
	state, err := db.LatestBoardGrant(ctx, a.dbPool, sessionID, boardID)
	if err != nil {
		return boardLevelOf{Level: db.BoardLevelPropose}, err
	}
	launch, err := a.launchBoardGrant(ctx, sessionID)
	if err != nil {
		return boardLevelOf{Level: db.BoardLevelPropose}, err
	}
	out := boardLevelOf{Level: db.BoardLevelPropose, State: state}
	if launch != nil && launch.BuiltinRef == boardID {
		out.Launch = launch
	}
	switch {
	case state != nil:
		out.Level = state.Level()
	case out.Launch != nil:
		out.Level = db.BoardLevelFull
	}
	return out, nil
}

// boardLevelView is one board with a level above propose, as the owner's browser and the session
// see it. The login session a level acts with is deliberately absent.
type boardLevelView struct {
	BoardID    string `json:"board_id"`
	BoardName  string `json:"board_name,omitempty"`
	Level      string `json:"level"`
	GrantID    string `json:"grant_id,omitempty"`
	CreatedVia string `json:"created_via,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	// Launch marks the board the session's launch gave a connection to (its engine's board tools).
	Launch bool `json:"launch,omitempty"`
	// Active is false when the sign-in the level was given from has ended (Inactive "signed_out"):
	// its reads, writes and automatic filings stop until it is given again.
	Active   bool   `json:"active"`
	Inactive string `json:"inactive,omitempty"`
	// For a session started by another one: OwnLevel is its own level and ParentLevel the level of
	// the session that started it now; Level is the lower of the two (possibly propose).
	OwnLevel    string `json:"own_level,omitempty"`
	ParentLevel string `json:"parent_level,omitempty"`
	// launchProof is the proof an untouched launch board acts with (no row of its own).
	launchProof mcpgw.Proof
	// child marks a started session's board; capProof is the parent's sign-in its calls act with.
	child    bool
	capProof mcpgw.Proof
}

// sessionBoardLevels lists the session's boards above propose, by name.
func (a *API) sessionBoardLevels(ctx context.Context, sessionID string) ([]boardLevelView, error) {
	states, err := db.ListLatestBoardGrants(ctx, a.dbPool, sessionID)
	if err != nil {
		return nil, err
	}
	launch, err := a.launchBoardGrant(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := []boardLevelView{}
	launchSeen := false
	for _, s := range states {
		isLaunch := launch != nil && launch.BuiltinRef == s.Grant.BoardID
		launchSeen = launchSeen || isLaunch
		if s.Level() == db.BoardLevelPropose {
			continue
		}
		out = append(out, boardLevelView{BoardID: s.Grant.BoardID, BoardName: s.Grant.BoardName, Level: s.Grant.Level, GrantID: s.Grant.ID,
			CreatedVia: s.Grant.CreatedVia, CreatedAt: s.Grant.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"), Launch: isLaunch, Active: true})
	}
	if launch != nil && !launchSeen {
		out = append(out, boardLevelView{BoardID: launch.BuiltinRef, Level: db.BoardLevelFull, CreatedVia: "launch",
			CreatedAt: launch.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"), Launch: true, Active: true,
			launchProof: mcpgw.ProofFromGrant(launch.AccountID, launch.ProofKind, launch.ProofValue)})
	}
	slices.SortFunc(out, func(x, y boardLevelView) int {
		return strings.Compare(x.BoardName+"\x00"+x.BoardID, y.BoardName+"\x00"+y.BoardID)
	})
	return out, nil
}

// setBoardLevel gives the board this level in the session, as the signed-in owner who. A level
// above propose needs the board to be one the person can reach now (their discovery, by id). It
// returns the row it stored (nil when nothing needed storing).
func (a *API) setBoardLevel(ctx context.Context, who grantRequester, row *db.SessionRow, boardID, level, via, fromProposal string) (*db.ProposalGrant, *APIError) {
	if who.Kind != string(identity.Human) || who.Account == "" || who.Sid == "" {
		return nil, apiErrorf(http.StatusUnauthorized, "%s", msgPermSignIn)
	}
	if row == nil || !ownsSession(who.Account, row) {
		return nil, apiErrorf(http.StatusNotFound, "not found")
	}
	if problem := proposalGrantProblem(row); problem != "" {
		return nil, apiErrorf(http.StatusConflict, "%s", problem)
	}
	if boardID == "" || !filingText(boardID, 200, false) {
		return nil, apiErrorf(http.StatusBadRequest, "a board permission names one board_id")
	}
	if who.Project != "" && boardID != who.Project {
		// A sign-in confined to one board changes that board's level only, lowering included.
		return nil, apiErrorf(http.StatusNotFound, "That board is not available to this account.")
	}
	if db.BoardLevelRank(level) < 0 {
		return nil, apiErrorf(http.StatusBadRequest, `level is "propose", "read", "file" or "full"`)
	}
	now, err := a.effectiveBoardLevel(ctx, row, boardID)
	if err != nil {
		log.Printf("board access: read level: %v", err)
		return nil, apiErrorf(http.StatusServiceUnavailable, "could not read the session's board access; retry")
	}
	if now.Child && db.BoardLevelRank(level) > db.BoardLevelRank(now.Cap) {
		// A started session is never given more than the session that started it has now.
		return nil, apiErrorf(http.StatusConflict, "%s: it has %s on this board, so this session can have at most %s.",
			msgChildAboveCap, boardLevelLabels[now.Cap], boardLevelLabels[now.Cap])
	}
	g := db.ProposalGrant{SessionID: row.ID, AccountID: who.Account, Kind: db.ProposalGrantBoard, BoardID: boardID, Level: level,
		ProofSessionID: who.Sid, ProjectScope: who.Project, CreatedVia: via, CreatedFromProposal: fromProposal}
	if now.State != nil {
		g.BoardName = now.State.Grant.BoardName
	}
	if level == db.BoardLevelPropose {
		// Propose is every board's default: a row is stored only to lower what holds above it.
		if now.Own == db.BoardLevelPropose {
			return nil, nil
		}
		if g.BoardName == "" {
			// An untouched launch board has no row to name it: ask discovery (best effort), so the
			// row and the agent's note carry the board's name, not its id.
			if boards, err := a.filingBoards(ctx, mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}, who.Project); err == nil {
				for _, b := range boards {
					if b.ID == boardID {
						g.BoardName = b.Name
					}
				}
			}
		}
	} else {
		boards, err := a.filingBoards(ctx, mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}, who.Project)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				return nil, apiErr
			}
			return nil, apiErrorf(http.StatusServiceUnavailable, "could not check your boards; retry")
		}
		found := false
		for _, b := range boards {
			if b.ID == boardID {
				g.BoardName, found = b.Name, true
			}
		}
		if !found {
			return nil, apiErrorf(http.StatusNotFound, "That board is not available to this account.")
		}
	}
	out, err := db.InsertProposalGrant(ctx, a.dbPool, g)
	if errors.Is(err, db.ErrProposalGrantSession) {
		return nil, apiErrorf(http.StatusConflict, "%s", msgPermNotActive)
	}
	if err != nil {
		log.Printf("board access: insert: %v", err)
		return nil, apiErrorf(http.StatusInternalServerError, "could not save the permission")
	}
	log.Printf("board access: session=%s account=%s board=%s level=%s->%s grant=%s via=%s", row.ID, who.Account, boardID, now.Own, out.Level, out.ID, via)
	if via != "launch" {
		name := boardDisplayName(out.BoardName, boardID)
		after := out.Level
		if now.Child {
			after = minBoardLevel(out.Level, now.Cap)
		}
		if after != now.Level {
			a.relayBoardAccess(ctx, who.Account, row, out.ID, name, now.Level, after)
		}
		// The sessions this one started follow its level (capped by their own).
		a.relayChildrenBoardAccess(ctx, who.Account, row, "board-access-"+out.ID, boardID, name, now.Level, out.Level)
	}
	return out, nil
}

var boardLevelLabels = map[string]string{db.BoardLevelPropose: "Propose", db.BoardLevelRead: "Read", db.BoardLevelFile: "File without asking", db.BoardLevelFull: "Full"}

// boardAccessNote is what the session's agent is told when the person changes a board's level.
func boardAccessNote(board, from, to string) string {
	head := fmt.Sprintf("Board access changed: board %s is now %s (was %s).", quotedName(board), boardLevelLabels[to], boardLevelLabels[from])
	switch {
	case db.BoardLevelRank(to) > db.BoardLevelRank(from):
		return head + " Use the blerg_board tool (or `blerg-runner board ...`) for what it allows; blerg_board with {\"tool\":\"access\"}" +
			" (or `blerg-runner board access`) lists your levels. Do not tell the person you lack access to it."
	case to == db.BoardLevelPropose:
		return head + " Reading and changing its cards is refused from your next call; you can still search it and propose cards with" +
			" blerg_ticket_file or `blerg-runner ticket file`, which ask the person."
	default:
		return head + " Calls the new level does not allow are refused from your next call; blerg_board with {\"tool\":\"access\"} lists your levels."
	}
}

// relayBoardAccess tells the session's agent that the person changed a board's level, as a note from
// Blerg (source "proposal", the relay of decisions; the API refuses that source from any caller, so
// an agent cannot write such a note itself). It arrives at the agent's next step, mid-turn where the
// engine supports steering. Best effort: the level holds whether or not the note is delivered.
func (a *API) relayBoardAccess(ctx context.Context, account string, row *db.SessionRow, grantID, board, from, to string) {
	a.relayBoardNote(ctx, account, row, "board-access-"+grantID, boardAccessNote(board, from, to))
}

// relayBoardNote delivers one board access note to the session's agent (relayBoardAccess);
// clientID makes a retry of the same change land once.
func (a *API) relayBoardNote(ctx context.Context, account string, row *db.SessionRow, clientID, text string) {
	if a.dbPool == nil || row == nil || row.Kind != "agent" || terminalSessionStatus(row.Status) {
		return
	}
	principal := runnerPrincipal{Kind: "human", Sub: account}
	var apiErr *APIError
	if dc := a.hub.FindDaemonForSession(row.ID); dc == nil || dc.CanInputReceipts() {
		apiErr = a.sendRelay(context.WithoutCancel(ctx), principal, row.ID, text, "proposal", clientID)
	} else {
		apiErr = a.sendRelay(context.WithoutCancel(ctx), principal, row.ID, text, "proposal")
	}
	if apiErr != nil {
		log.Printf("board access: tell %s: %s", row.ID, apiErr.Message)
	}
}

// HandleSetBoardAccess is PUT /api/sessions/{id}/board-access {"board_id","level"}: the owner sets
// one board's level. It answers the session's boards as they are now.
func (a *API) HandleSetBoardAccess(w http.ResponseWriter, r *http.Request) {
	who, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	row, ok := a.ownedSession(r.Context(), w, who.Account, r.PathValue("id"))
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	var req struct {
		BoardID string `json:"board_id"`
		Level   string `json:"level"`
	}
	if err != nil || filingDecode(raw, &req) != nil {
		writeError(w, http.StatusBadRequest, `the body is {"board_id":…,"level":"propose"|"read"|"file"|"full"}`)
		return
	}
	if _, apiErr := a.setBoardLevel(r.Context(), who, row, req.BoardID, req.Level, "panel", ""); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	boards, err := a.boardLevelsFor(r.Context(), row)
	if err != nil {
		writeDatabaseFailure(w, "read the session's boards", "board levels", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"boards": boards})
}

// ---- the session's own board tools ---------------------------------------------------------------

// boardSession authenticates a session's own board request: the session's own token (a session or
// runtime token of exactly this session; never the daemon's master token, an agent token or a
// person), on a live session that may hold board permissions.
func (a *API) boardSession(w http.ResponseWriter, r *http.Request) (*db.SessionRow, bool) {
	sid := r.PathValue("id")
	tokenSID, ok := a.authMessaging(w, r)
	if !ok {
		return nil, false
	}
	if tokenSID == "" || tokenSID != sid {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	// Counted before any work beyond the token check, refused requests included.
	if after := boardCallRetryAfter(sid, time.Now()); after > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(after))
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf(msgBoardCallsPerMinute, after))
		return nil, false
	}
	if a.dbPool == nil || !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "Session not found.")
		return nil, false
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil || row == nil {
		writeError(w, http.StatusNotFound, "Session not found.")
		return nil, false
	}
	if terminalSessionStatus(row.Status) {
		writeError(w, http.StatusConflict, "This session has ended.")
		return nil, false
	}
	if proposalGrantProblem(row) != "" {
		writeError(w, http.StatusConflict, msgBoardToolsSession)
		return nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return row, true
}

// HandleSessionBoardAccess is GET /api/sessions/{id}/board-access for the session itself: the boards
// it may read or change now, with their levels (propose, the default for every board, is not
// listed). The CLI picks its default board from it.
func (a *API) HandleSessionBoardAccess(w http.ResponseWriter, r *http.Request) {
	row, ok := a.boardSession(w, r)
	if !ok {
		return
	}
	boards, err := a.boardLevelsFor(r.Context(), row)
	if err != nil {
		writeDatabaseFailure(w, "read the session's boards", "board levels", err)
		return
	}
	type entry struct {
		ID    string `json:"id"`
		Name  string `json:"name,omitempty"`
		Level string `json:"level"`
		// A started session's level is capped by the session that started it.
		ParentLevel string `json:"parent_level,omitempty"`
	}
	out := []entry{}
	for _, b := range boards {
		if b.Level == db.BoardLevelPropose {
			continue
		}
		out = append(out, entry{ID: b.BoardID, Name: b.BoardName, Level: b.Level, ParentLevel: b.ParentLevel})
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": a.sessionBoardCaller() != nil, "boards": out})
}

func (a *API) sessionBoardCaller() sessionBoardCaller {
	cfg := a.hub.mcpStart.get()
	if cfg == nil || cfg.Board == nil || !cfg.Board.BoardConfigured() {
		return nil
	}
	c, _ := cfg.Board.(sessionBoardCaller)
	return c
}

// boardArgTools take the board as their board_id argument; the call's board is put there when it is
// missing. Every other tool names a card (the board scopes it with the exchanged credential) or, for
// blerg_board_list, lists only that credential's board.
var boardArgTools = map[string]bool{
	"blerg_board_get": true, "blerg_board_schema": true, "blerg_column_list": true,
	"blerg_card_search": true, "blerg_card_create": true,
}

// boardToolRequest is the body of POST /api/sessions/{id}/board-tools.
type boardToolRequest struct {
	Board     string          `json:"board"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// boardCallLimit bounds one session's board calls (each is a core exchange and a revoke).
var boardCallLimit = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: map[string][]time.Time{}}

// Rejected retries are not recorded: they never move the rolling one-minute window.
func boardCallRetryAfter(sessionID string, now time.Time) int {
	boardCallLimit.Lock()
	defer boardCallLimit.Unlock()
	cut := now.Add(-time.Minute)
	recent := boardCallLimit.m[sessionID][:0]
	for _, t := range boardCallLimit.m[sessionID] {
		if t.After(cut) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= boardCallsPerMinute {
		boardCallLimit.m[sessionID] = recent
		return int((recent[0].Add(time.Minute).Sub(now) + time.Second - 1) / time.Second)
	}
	boardCallLimit.m[sessionID] = append(recent, now)
	if len(boardCallLimit.m) > 10000 {
		for k, v := range boardCallLimit.m {
			if len(v) == 0 || !v[len(v)-1].After(cut) {
				delete(boardCallLimit.m, k)
			}
		}
	}
	return 0
}

// runSessionBoardTool is one call of the session's board tools: the board (an id, or the exact name
// of one of the session's boards above propose), the tool and its arguments. It answers the status
// and the body: the tool's sanitized MCP result, or {"error": …}.
func (a *API) runSessionBoardTool(ctx context.Context, row *db.SessionRow, req boardToolRequest) (int, any) {
	fail := func(status int, msg string) (int, any) { return status, map[string]string{"error": msg} }
	caller := a.sessionBoardCaller()
	if caller == nil {
		return fail(http.StatusServiceUnavailable, msgBoardToolsUnavailable)
	}
	need := mcpgw.BoardToolLevel(req.Tool)
	if need == "" {
		return fail(http.StatusBadRequest, "unknown board tool "+capText(req.Tool, 64)+"; the board tools are "+strings.Join(mcpgw.BoardToolNames(), ", "))
	}
	if req.Board == "" || !filingText(req.Board, 200, false) {
		return fail(http.StatusBadRequest, "name the board (its id, or its name as blerg-runner board access lists it)")
	}
	boards, err := a.boardLevelsFor(ctx, row)
	if err != nil {
		log.Printf("board tools: levels: %v", err)
		return fail(http.StatusServiceUnavailable, "could not read this session's board access; retry")
	}
	boardID := req.Board
	if !gatewayServerName.MatchString(boardID) || !slices.ContainsFunc(boards, func(b boardLevelView) bool { return b.BoardID == boardID }) {
		var named []string
		for _, b := range boards {
			if b.BoardName == req.Board && b.Level != db.BoardLevelPropose {
				named = append(named, b.BoardID)
			}
		}
		switch {
		case len(named) == 1:
			boardID = named[0]
		case len(named) > 1:
			return fail(http.StatusUnprocessableEntity, "more than one board has that name; use its id")
		case !gatewayServerName.MatchString(boardID):
			return fail(http.StatusForbidden, mcpgw.BoardLevelRefusal(capText(req.Board, 64), db.BoardLevelPropose, req.Tool))
		}
	}
	args := map[string]json.RawMessage{}
	if trimmed := bytes.TrimSpace(req.Arguments); len(trimmed) > 0 && string(trimmed) != "null" {
		if len(trimmed) > maxBoardToolArgsBytes || trimmed[0] != '{' || json.Unmarshal(trimmed, &args) != nil {
			return fail(http.StatusBadRequest, "arguments must be a JSON object of at most 64 KiB")
		}
	}
	if raw, ok := args["board_id"]; ok {
		var named string
		if json.Unmarshal(raw, &named) != nil || named != boardID {
			return fail(http.StatusBadRequest, "arguments.board_id must be the call's board")
		}
	} else if boardArgTools[req.Tool] {
		args["board_id"], _ = json.Marshal(boardID)
	}
	// The level, read now: a change the person made holds at this call.
	lv, err := a.effectiveBoardLevel(ctx, row, boardID)
	if err != nil {
		log.Printf("board tools: level: %v", err)
		return fail(http.StatusServiceUnavailable, "could not read this session's board access; retry")
	}
	if !mcpgw.BoardLevelAllows(lv.Level, req.Tool) {
		log.Printf("board tools: session=%s board=%s tool=%s level=%s outcome=refused", row.ID, boardID, req.Tool, lv.Level)
		msg := mcpgw.BoardLevelRefusal(boardID, lv.Level, req.Tool)
		if lv.Child && db.BoardLevelRank(lv.Own) > db.BoardLevelRank(lv.Cap) {
			msg += fmt.Sprintf(" Your level (%s) is capped by the session that started you, which has %s on this board now.",
				boardLevelLabels[lv.Own], boardLevelLabels[lv.Cap])
		}
		return fail(http.StatusForbidden, msg)
	}
	proof, ok := lv.proof()
	if !ok || proof.AccountID == "" || row.SpawningAccountID == nil || *row.SpawningAccountID != proof.AccountID {
		return fail(http.StatusForbidden, "this board access has no sign-in to act with; ask the person to set it again in Permissions")
	}
	argsRaw, _ := json.Marshal(args)
	res, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: boardID, SessionRef: row.ID, Tool: req.Tool,
		Arguments: argsRaw, ReadOnly: need == db.BoardLevelRead})
	if err != nil {
		log.Printf("board tools: session=%s board=%s tool=%s level=%s outcome=failed", row.ID, boardID, req.Tool, lv.Level)
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, mcpgw.ErrConnectionGone):
			status = http.StatusForbidden
		case errors.Is(err, mcpgw.ErrBoardRevokesFailing):
			status = http.StatusServiceUnavailable
		}
		return fail(status, mcpgw.UpstreamMessage(err))
	}
	log.Printf("board tools: session=%s board=%s tool=%s level=%s outcome=ok", row.ID, boardID, req.Tool, lv.Level)
	return http.StatusOK, res
}

// boardInFlight holds one slot set per session with a request in flight.
var boardInFlight = struct {
	sync.Mutex
	m map[string]*boardSlots
}{m: map[string]*boardSlots{}}

type boardSlots struct {
	ch    chan struct{}
	users int
}

// acquireBoardCall takes one of the session's boardCallsInFlight slots, waiting up to boardCallWait.
func acquireBoardCall(ctx context.Context, sessionID string) (release func(), ok bool) {
	boardInFlight.Lock()
	s := boardInFlight.m[sessionID]
	if s == nil {
		s = &boardSlots{ch: make(chan struct{}, boardCallsInFlight)}
		boardInFlight.m[sessionID] = s
	}
	s.users++
	boardInFlight.Unlock()
	done := func() {
		boardInFlight.Lock()
		s.users--
		if s.users == 0 && boardInFlight.m[sessionID] == s {
			delete(boardInFlight.m, sessionID)
		}
		boardInFlight.Unlock()
	}
	t := time.NewTimer(boardCallWait)
	defer t.Stop()
	select {
	case s.ch <- struct{}{}:
		return func() { <-s.ch; done() }, true
	case <-t.C:
	case <-ctx.Done():
	}
	done()
	return nil, false
}

func capText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// HandleSessionBoardTool is POST /api/sessions/{id}/board-tools: one board tool call of the session,
// at its current level for that board.
func (a *API) HandleSessionBoardTool(w http.ResponseWriter, r *http.Request) {
	row, ok := a.boardSession(w, r)
	if !ok {
		return
	}
	release, ok := acquireBoardCall(r.Context(), row.ID)
	if !ok {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, msgBoardCallsAtOnce)
		return
	}
	defer release()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBoardToolArgsBytes+4096))
	var req boardToolRequest
	if err != nil || filingDecode(raw, &req) != nil {
		writeError(w, http.StatusBadRequest, `the body is {"board":…,"tool":…,"arguments":{…}}`)
		return
	}
	status, body := a.runSessionBoardTool(r.Context(), row, req)
	writeJSON(w, status, body)
}

// boardSessionToolDefinition is the blerg_board tool of the session's built-in MCP server. It is
// always listed, so a level given while the engine runs is usable at once; each call is checked.
func boardSessionToolDefinition() any {
	str := map[string]string{"type": "string"}
	tools := append([]string{"access"}, mcpgw.BoardToolNames()...)
	return map[string]any{"name": "blerg_board", "description": "Read or change cards on a board your person gave this session access to in its Permissions. " +
		"Call {\"tool\":\"access\"} first: it lists the boards and their level. Level read: blerg_board_get, blerg_board_list, blerg_board_schema, " +
		"blerg_column_list, blerg_card_search, blerg_card_get. Level full adds blerg_card_create, blerg_card_update, blerg_card_move, blerg_card_comment, " +
		"blerg_card_link. arguments are that board tool's own arguments. The person can change levels while you work: if a board tool you need is not " +
		"listed or a call is refused, call this tool with {\"tool\":\"access\"} before telling the person you lack permission, and use it for anything the " +
		"level allows. Without access, use blerg_ticket_file to propose a card; it asks the person.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"tool"}, "properties": map[string]any{
			"tool": map[string]any{"type": "string", "enum": tools}, "board": str, "arguments": map[string]any{"type": "object"}}}}
}

// RegisterSessionBoardRoutes mounts the board access routes.
func (a *API) RegisterSessionBoardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/sessions/{id}/board-access", a.HandleSetBoardAccess)
	mux.HandleFunc("GET /api/sessions/{id}/board-access", a.HandleSessionBoardAccess)
	mux.HandleFunc("POST /api/sessions/{id}/board-tools", a.HandleSessionBoardTool)
	mux.HandleFunc("GET /api/board-cards/{board}/{number}", a.HandleGetLiveBoardCard)
	mux.HandleFunc("POST /api/board-cards/{board}/{number}/move", a.HandleMoveLiveBoardCard)
	mux.HandleFunc("GET /api/sessions/{id}/board-card-url", a.HandleSessionBoardCardURL)
}
