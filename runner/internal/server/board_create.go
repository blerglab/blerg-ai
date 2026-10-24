package server

// A session proposes a new board (blerg-runner board create, POST /api/sessions/{id}/board-create).
// Nothing is created then: the proposal waits in Needs you like a ticket filing. The person reads
// the name, repositories, columns or template and the access the session asks for, may lower that
// access, and approves. Approval creates the board with the PERSON's authority (core issues a
// one-board capability only to a live administrator's sign-in, bound to the exact approved draft;
// Board consumes it once), then gives the proposing session, and only it, access to the new board
// at the approved level, through the same board permission the Permissions dialog sets (capped for
// a started session by the session that started it). Rejection or expiry creates nothing. A
// session never holds board.admin and cannot approve its own proposal: every decision route is a
// signed-in person's, owner only.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// boardCreateArguments are a board proposal's frozen arguments: the draft, the access the session
// asks for, and the CLI's idempotency key.
type boardCreateArguments struct {
	boardfiling.BoardDraft
	Access         string `json:"access"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// createdBoard is the result of an approved board proposal: the new board and the access the
// session was given (propose when none could be given; AccessNote says why).
type createdBoard struct {
	ID         string   `json:"board_id"`
	Name       string   `json:"board_name"`
	Repos      []string `json:"repos"`
	Requested  string   `json:"requested_access"`
	Access     string   `json:"access"`
	AccessNote string   `json:"access_note,omitempty"`
}

// boardCreator creates an approved board with the approver's authority (httpBoardFiling).
type boardCreator interface {
	CreateBoard(ctx context.Context, proof mcpgw.Proof, intent string, draft boardfiling.BoardDraft) (createdBoard, error)
}

// errBoardApprovalUsed: Board says this approval's one-board credential was already used (its board
// was created before, perhaps then deleted). Final: approving again can never create a board.
var errBoardApprovalUsed = errors.New("board approval already used")

const (
	msgBoardApprovalUsed = "This approval was already used to create a board (perhaps since deleted); no new board was created. Check the Board app."
	msgCoreTooOld        = "Blerg core is older than this runner and cannot approve boards yet; update core, then approve again."
	msgBoardTooOld       = "Blerg Board is older than this runner and cannot create approved boards yet; update Board, then approve again."
	msgBoardClock        = "Board could not verify the approval: the servers' clocks may differ by more than 30 seconds, or your sign-in ended. Check the clocks, sign in again if needed, and retry."
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

// boardAccessLevels are the levels a proposed board's session may ask for.
var boardAccessLevels = []string{db.BoardLevelRead, db.BoardLevelFile, db.BoardLevelFull}

func (a *API) boardCreator() (boardFilingUpstream, boardCreator, error) {
	up, err := a.filingUpstream()
	if err != nil {
		return nil, nil, err
	}
	c, ok := up.(boardCreator)
	if !ok {
		return nil, nil, apiErrorf(http.StatusServiceUnavailable, "Board creation is not configured on this runner.")
	}
	return up, c, nil
}

// HandleProposeBoard is POST /api/sessions/{id}/board-create: the session's own token proposes a
// board. It answers 202 with the proposal (or the earlier one this repeats).
func (a *API) HandleProposeBoard(w http.ResponseWriter, r *http.Request) {
	sid, body, ok := a.agentSessionWrite(w, r, "board proposals", 32<<10)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" && !idempotencyKeyPattern.MatchString(key) {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is 8 to 128 letters, digits, '_', '.', ':' or '-'.")
		return
	}
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Repos       []string `json:"repos"`
		Template    string   `json:"template"`
		Columns     []string `json:"columns"`
		Access      string   `json:"access"`
	}
	if filingDecode(body, &req) != nil {
		writeError(w, http.StatusBadRequest, `Provide {"name", "description", "repos", "template", "columns", "access"}.`)
		return
	}
	if req.Access == "" {
		req.Access = db.BoardLevelFull
	}
	if !slices.Contains(boardAccessLevels, req.Access) {
		writeError(w, http.StatusBadRequest, `access is "read", "file" or "full".`)
		return
	}
	args := boardCreateArguments{BoardDraft: boardfiling.BoardDraft{Name: req.Name, Description: req.Description, Repos: req.Repos, Template: req.Template, Columns: req.Columns},
		Access: req.Access, IdempotencyKey: key}
	if err := boardfiling.ValidateBoardDraft(args.BoardDraft); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid board: "+err.Error()+".")
		return
	}
	if err := validateRepoNames(args.Repos); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid board: "+err.Error()+".")
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil || row == nil || derefOrEmpty(row.SpawningAccountID) == "" || row.Kind != "agent" {
		writeError(w, http.StatusConflict, "This session cannot propose boards.")
		return
	}
	if problem := proposalGrantProblem(row); problem != "" {
		writeError(w, http.StatusConflict, "This session cannot be given access to a new board ("+strings.TrimRight(problem, ".")+"); ask the person to create it in the Board app.")
		return
	}
	up, _, err := a.boardCreator()
	if err != nil {
		writeFilingError(w, err)
		return
	}
	raw, _ := json.Marshal(args)
	proposal, err := db.InsertProposalLimited(r.Context(), a.dbPool, db.Proposal{AccountID: *row.SpawningAccountID, SessionID: sid, ConnectionID: db.BoardCreateConnectionID,
		ConnectionName: "board-create", Tool: "create_board", ToolHash: "board-create-v1", URLSnapshot: up.SnapshotURL(), Arguments: raw,
		AgentSummary: "Create a board after the person's approval."}, db.ProposalLimits{Account: 50, Session: 20, Cron: 20})
	if errors.Is(err, db.ErrProposalIdempotency) {
		writeError(w, http.StatusConflict, "That idempotency key was already used for a different board proposal; propose without it, or with a new key.")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "The board proposal could not be queued; check pending proposal limits and retry.")
		return
	}
	if !proposal.Repeated {
		a.announceProposalRequest(r.Context(), proposal, "")
	}
	out := map[string]any{"proposal_id": proposal.ID, "url": boardBaseURL(os.Getenv("BLERG_RUNNER_SELF_URL")) + "/proposals?id=" + proposal.ID, "state": proposal.State}
	if proposal.Repeated {
		out["repeated"] = true
	}
	if b := readCreatedBoard(proposal); b != nil {
		out["board"] = b
	}
	writeJSON(w, http.StatusAccepted, out)
}

// readCreatedBoard is the board an approved board proposal created, or nil.
func readCreatedBoard(p *db.Proposal) *createdBoard {
	if !db.IsBoardCreateProposal(p) || p.State != db.ProposalDone {
		return nil
	}
	var b createdBoard
	if json.Unmarshal(p.Result, &b) != nil || b.ID == "" {
		return nil
	}
	return &b
}

// approveBoardCreate is the person's approval of a proposed board: {"expected_arguments_raw": the
// exact stored arguments reviewed, "access": optionally a lower level than the session asked for}.
func (h *proposalsHandler) approveBoardCreate(ctx context.Context, w http.ResponseWriter, who grantRequester, p *db.Proposal, raw []byte) {
	var body struct {
		Expected string `json:"expected_arguments_raw"`
		Access   string `json:"access"`
	}
	if len(bytes.TrimSpace(raw)) == 0 || filingDecode(raw, &body) != nil || body.Expected == "" || body.Expected != string(p.Arguments) {
		writeError(w, http.StatusConflict, "The board proposal changed; reload it and review it before approving.")
		return
	}
	if p.State != db.ProposalPending {
		writeError(w, http.StatusConflict, "this proposal is no longer pending ("+p.State+")")
		return
	}
	if !time.Now().Before(p.Deadline()) {
		writeError(w, http.StatusConflict, "this proposal has expired and can no longer be approved: let the agent propose again")
		return
	}
	if who.Project != "" {
		writeError(w, http.StatusForbidden, "This sign-in is confined to one board, so it cannot create boards.")
		return
	}
	var args boardCreateArguments
	if json.Unmarshal(p.Arguments, &args) != nil || boardfiling.ValidateBoardDraft(args.BoardDraft) != nil || validateRepoNames(args.Repos) != nil || !slices.Contains(boardAccessLevels, args.Access) {
		writeError(w, http.StatusConflict, "The saved board proposal is invalid; reject it.")
		return
	}
	level := args.Access
	if body.Access != "" {
		if !slices.Contains(boardAccessLevels, body.Access) && body.Access != db.BoardLevelPropose {
			writeError(w, http.StatusBadRequest, `access is "propose", "read", "file" or "full".`)
			return
		}
		if db.BoardLevelRank(body.Access) > db.BoardLevelRank(args.Access) {
			writeError(w, http.StatusBadRequest, "You can lower the access the session asked for, not raise it.")
			return
		}
		level = body.Access
	}
	row, err := db.GetSession(ctx, h.api.dbPool, p.SessionID)
	if err != nil || row == nil || !ownsSession(who.Account, row) {
		writeError(w, http.StatusConflict, "The session that proposed this board is gone; reject it.")
		return
	}
	up, creator, err := h.api.boardCreator()
	if err != nil {
		writeFilingError(w, err)
		return
	}
	if up.SnapshotURL() != p.URLSnapshot {
		writeError(w, http.StatusConflict, "The Board destination changed; reject this and let the agent propose again.")
		return
	}
	casCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), proposalWriteBudget)
	defer cancel()
	if _, err := db.BeginBoardCreateExecution(casCtx, h.api.dbPool, who.Account, p.ID, body.Expected); err != nil {
		transitionError(w, err, "The board proposal changed or was already decided.")
		return
	}
	log.Printf("proposals: approved board creation id=%s account=%s session=%s", p.ID, p.AccountID, p.SessionID)
	proof := mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}
	runCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	board, err := creator.CreateBoard(runCtx, proof, p.ID, args.BoardDraft)
	recCtx, stopRec := context.WithTimeout(context.WithoutCancel(ctx), proposalWriteBudget)
	defer stopRec()
	if errors.Is(err, errBoardApprovalUsed) {
		result, _ := json.Marshal(map[string]string{"error": msgBoardApprovalUsed})
		done, ferr := db.FinishProposal(recCtx, h.api.dbPool, p.ID, db.ProposalFailed, result)
		if ferr != nil {
			writeError(w, http.StatusInternalServerError, "The outcome could not be recorded; check the Board app before resolving this proposal.")
			return
		}
		writeJSON(w, http.StatusOK, viewOfProposal(done))
		return
	}
	if err != nil {
		var refusal *APIError
		if errors.As(err, &refusal) {
			// A definite refusal created no board: the proposal waits again, and the person sees why.
			if _, releaseErr := db.ReleaseProposal(recCtx, h.api.dbPool, p.ID); releaseErr == nil {
				writeFilingError(w, err)
				return
			}
		}
		result := json.RawMessage(`{"error":"The board could not be confirmed; check the Board app before resolving this proposal."}`)
		done, ferr := db.FinishProposal(recCtx, h.api.dbPool, p.ID, db.ProposalUnknown, result)
		if ferr != nil {
			writeError(w, http.StatusInternalServerError, "The outcome could not be recorded; check the Board app before resolving this proposal.")
			return
		}
		writeJSON(w, http.StatusOK, viewOfProposal(done))
		return
	}
	board.Requested = args.Access
	board.Access, board.AccessNote = h.api.grantNewBoard(recCtx, who, row, board.ID, level, p.ID)
	result, _ := json.Marshal(board)
	done, err := db.FinishProposal(recCtx, h.api.dbPool, p.ID, db.ProposalDone, result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "The board was created, but the outcome could not be recorded; check the Board app before resolving this proposal.")
		return
	}
	log.Printf("proposals: created board id=%s board=%s session=%s access=%s", p.ID, board.ID, p.SessionID, board.Access)
	writeJSON(w, http.StatusOK, viewOfProposal(done))
}

// grantNewBoard gives the proposing session level on the new board, as the approving person, and
// answers the level it has now (propose when none was given) and why it is lower than asked.
func (a *API) grantNewBoard(ctx context.Context, who grantRequester, row *db.SessionRow, boardID, level, proposalID string) (string, string) {
	if level == db.BoardLevelPropose {
		return db.BoardLevelPropose, "You chose not to give the session access."
	}
	fresh, err := db.GetSession(ctx, a.dbPool, row.ID)
	if err == nil && fresh != nil {
		row = fresh
	}
	if problem := proposalGrantProblem(row); problem != "" {
		return db.BoardLevelPropose, "The session could not be given access: " + problem
	}
	note := ""
	now, err := a.effectiveBoardLevel(ctx, row, boardID)
	if err != nil {
		return db.BoardLevelPropose, "The session's access could not be checked; set it in the session's Permissions."
	}
	if now.Child && db.BoardLevelRank(level) > db.BoardLevelRank(now.Cap) {
		level = now.Cap
		note = fmt.Sprintf("Capped at %s by the session that started this one.", boardLevelLabels[level])
		if level == db.BoardLevelPropose {
			return level, note
		}
	}
	if _, apiErr := a.setBoardLevel(ctx, who, row, boardID, level, "card", proposalID); apiErr != nil {
		return db.BoardLevelPropose, "The session could not be given access: " + apiErr.Message
	}
	return level, note
}

// CreateBoard creates an approved board: core issues the one-board capability for this exact
// draft to the approving person (an administrator, signed in), and Board consumes it.
func (c *httpBoardFiling) CreateBoard(ctx context.Context, proof mcpgw.Proof, intent string, draft boardfiling.BoardDraft) (createdBoard, error) {
	if proof.AccountID == "" || proof.SessionID == "" || proof.TokenID != "" {
		return createdBoard{}, apiErrorf(http.StatusForbidden, "A signed-in person must approve this board.")
	}
	raw, _ := json.Marshal(map[string]string{"account_id": proof.AccountID, "session_id": proof.SessionID, "intent": intent, "digest": boardfiling.BoardDigest(draft)})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.core.BaseURL, "/")+"/internal/tokens/exchange/board-new", bytes.NewReader(raw))
	if err != nil {
		return createdBoard{}, apiErrorf(http.StatusServiceUnavailable, "Board approval is unavailable.")
	}
	req.Header.Set("X-Internal-Key", c.core.InternalKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.core.httpClient().Do(req)
	if err != nil {
		return createdBoard{}, apiErrorf(http.StatusServiceUnavailable, "Board approval could not verify your access; retry.")
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return createdBoard{}, apiErrorf(http.StatusForbidden, "Only an administrator can create boards: ask one to approve it, or reject it.")
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		// core answers "this sign-in is not live" in JSON; a core without the route answers the
		// mux's plain-text 404 (or 405), which is not the person's sign-in.
		if resp.StatusCode == http.StatusNotFound && strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
			return createdBoard{}, apiErrorf(http.StatusUnauthorized, "%s", msgProposalSignIn)
		}
		return createdBoard{}, apiErrorf(http.StatusServiceUnavailable, "%s", msgCoreTooOld)
	default:
		return createdBoard{}, apiErrorf(http.StatusServiceUnavailable, "Board approval could not verify your access; retry.")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<10)+1))
	var tok struct {
		Token string `json:"token"`
	}
	if err != nil || len(data) > 16<<10 || json.Unmarshal(data, &tok) != nil || tok.Token == "" || len(tok.Token) > 8<<10 || strings.ContainsAny(tok.Token, "\r\n \t") {
		return createdBoard{}, apiErrorf(http.StatusServiceUnavailable, "Board approval credential unavailable; retry.")
	}
	receiptRaw, err := c.boardRequest(ctx, "POST", "/api/boards/create-approved", tok.Token, draft)
	var refusal *APIError
	if errors.As(err, &refusal) {
		switch refusal.Status {
		case http.StatusConflict:
			return createdBoard{}, errBoardApprovalUsed
		case http.StatusUnauthorized:
			// The credential lives 30 seconds from core's clock: a Board clock ahead by more than that,
			// or a sign-in revoked on the way, reads as an expired or revoked token.
			return createdBoard{}, apiErrorf(http.StatusUnauthorized, "%s", msgBoardClock)
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			return createdBoard{}, apiErrorf(http.StatusServiceUnavailable, "%s", msgBoardTooOld)
		}
	}
	if err != nil {
		return createdBoard{}, err
	}
	var receipt struct {
		ID    string   `json:"id"`
		Name  string   `json:"name"`
		Repos []string `json:"repos"`
	}
	if json.Unmarshal(receiptRaw, &receipt) != nil || !validSessionID(receipt.ID) || receipt.Name != draft.Name {
		return createdBoard{}, mcpgw.ErrOutcomeUnknown
	}
	if receipt.Repos == nil {
		receipt.Repos = []string{}
	}
	return createdBoard{ID: receipt.ID, Name: receipt.Name, Repos: receipt.Repos}, nil
}

// boardRepoChoice is one repository the connected desktops have checked out, for the Board app's
// New board picker: Repo is what a board stores (owner/name when the desktops agree on the
// folder's remote, else the folder name).
type boardRepoChoice struct {
	Repo     string   `json:"repo"`
	Folder   string   `json:"folder"`
	Desktops []string `json:"desktops"`
}

// HandleBoardRepoChoices is GET /api/board-repo-choices, for the Board server only (the runner
// key it holds): the repositories the connected desktops have checked out. A signed-in person
// sees the same folders in the launch sheet's repository list (GET /api/repos).
func (a *API) HandleBoardRepoChoices(w http.ResponseWriter, r *http.Request) {
	if raw := a.rawBearer(r); raw == "" || !a.isDaemonToken(raw) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	owner := a.boardDesktopViewer(r)
	if owner == "" {
		writeError(w, http.StatusUnauthorized, "signed person proof required")
		return
	}
	daemons := a.hub.ownedHostDaemons(r.Context(), owner)
	byRepo := map[string]*boardRepoChoice{}
	for _, d := range daemons {
		for _, folder := range d.CheckedOutRepos() {
			repo := folder
			if o, ok := agreedOrigin(daemons, folder); ok && o.FullName != "" {
				repo = o.FullName
			}
			if !boardfiling.ValidRepoName(repo) {
				continue
			}
			c := byRepo[repo]
			if c == nil {
				c = &boardRepoChoice{Repo: repo, Folder: folder, Desktops: []string{}}
				byRepo[repo] = c
			}
			if !slices.Contains(c.Desktops, d.Name) {
				c.Desktops = append(c.Desktops, d.Name)
			}
		}
	}
	out := make([]boardRepoChoice, 0, len(byRepo))
	for _, c := range byRepo {
		sort.Strings(c.Desktops)
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Repo) < strings.ToLower(out[j].Repo) })
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}
