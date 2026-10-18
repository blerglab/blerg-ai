package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type filingArguments struct {
	BoardID   string `json:"board_id"`
	BoardName string `json:"board_name"`
	boardfiling.Draft
}

func filingDecode(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func filingText(s string, maxRunes int, allowLines bool) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRunes {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && (!allowLines || (r != '\n' && r != '\r' && r != '\t')) {
			return false
		}
	}
	return true
}
func validateFilingDraft(d boardfiling.Draft) error {
	if strings.TrimSpace(d.Title) == "" || !filingText(d.Title, 200, false) || len(d.Body) > 64<<10 || !filingText(d.Body, 64<<10, true) || !filingText(d.Type, 64, false) || len(d.Repos) > 20 {
		return apiErrorf(400, "Provide a title up to 200 characters, body up to 64 KiB, and valid type and repositories.")
	}
	for _, repo := range d.Repos {
		if repo == "" || !filingText(repo, 512, false) {
			return apiErrorf(400, "Target repositories must be nonempty and at most 512 characters.")
		}
	}
	return nil
}
func writeFilingError(w http.ResponseWriter, err error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		writeAPIError(w, apiErr)
	} else {
		writeError(w, 503, "Board filing is unavailable; retry.")
	}
}
func (a *API) filingRead(w http.ResponseWriter, r *http.Request) (*db.SessionRow, mcpgw.Proof, string, bool) {
	sid := r.PathValue("id")
	tokenSID, ok := a.authMessaging(w, r)
	if !ok {
		return nil, mcpgw.Proof{}, "", false
	}
	if tokenSID != "" && tokenSID != sid {
		writeError(w, 403, "forbidden")
		return nil, mcpgw.Proof{}, "", false
	}
	if a.dbPool == nil || !validSessionID(sid) {
		writeError(w, 404, "Session not found.")
		return nil, mcpgw.Proof{}, "", false
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil || row == nil {
		writeError(w, 404, "Session not found.")
		return nil, mcpgw.Proof{}, "", false
	}
	if terminalSessionStatus(row.Status) {
		writeError(w, 409, "This session has ended.")
		return nil, mcpgw.Proof{}, "", false
	}
	p, project, err := a.filingProof(r.Context(), row)
	if err != nil {
		writeFilingError(w, err)
		return nil, p, "", false
	}
	w.Header().Set("Cache-Control", "no-store")
	return row, p, project, true
}
func (a *API) filingBoards(ctx context.Context, p mcpgw.Proof, project string) ([]SessionBoard, error) {
	boards, err := a.sessionBoards(ctx, p)
	if err != nil {
		return nil, err
	}
	if project == "" {
		return boards, nil
	}
	out := []SessionBoard{}
	for _, b := range boards {
		if b.ID == project {
			out = append(out, b)
		}
	}
	return out, nil
}
func selectFilingBoard(boards []SessionBoard, name string) (SessionBoard, error) {
	for _, b := range boards {
		if b.ID == name {
			return b, nil
		}
	}
	var found SessionBoard
	count := 0
	for _, b := range boards {
		if strings.EqualFold(b.Name, name) {
			found = b
			count++
		}
	}
	if count == 1 {
		return found, nil
	}
	if count > 1 {
		return found, apiErrorf(422, "More than one board has that name; use its ID.")
	}
	return found, apiErrorf(404, "That board is not available to this account.")
}
func (a *API) HandleFilingBoards(w http.ResponseWriter, r *http.Request) {
	_, p, project, ok := a.filingRead(w, r)
	if !ok {
		return
	}
	boards, err := a.filingBoards(r.Context(), p, project)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"configured": true, "boards": boards})
}
func (a *API) HandleFilingSearch(w http.ResponseWriter, r *http.Request) {
	_, p, project, ok := a.filingRead(w, r)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	if !filingText(q, 1000, false) {
		writeError(w, 400, "Search is limited to 1000 characters.")
		return
	}
	boards, err := a.filingBoards(r.Context(), p, project)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	board, err := selectFilingBoard(boards, r.URL.Query().Get("board"))
	if err != nil {
		writeFilingError(w, err)
		return
	}
	up, err := a.filingUpstream()
	if err != nil {
		writeFilingError(w, err)
		return
	}
	raw, err := up.Search(r.Context(), p, board.ID, q)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, raw)
}
func (a *API) filingOrigin(ctx context.Context, row *db.SessionRow) (boardfiling.Origin, error) {
	base := boardBaseURL(os.Getenv("BLERG_RUNNER_SELF_URL"))
	if base == "" {
		return boardfiling.Origin{}, apiErrorf(503, "Configure the runner's public URL before filing tickets.")
	}
	lookup := a.sessionOriginLookup
	if lookup == nil {
		lookup = a.readSessionOrigin
	}
	commit, err := lookup(ctx, row.ID)
	if err != nil {
		return boardfiling.Origin{}, apiErrorf(409, "The session's workspace is unavailable; retry when its daemon reconnects.")
	}
	return boardfiling.Origin{SessionURL: base + "/sessions/" + row.ID, Repo: row.Repo, Commit: commit, Engine: engines.Normalize(derefOrEmpty(row.Engine))}, nil
}
func (a *API) HandleFileTicket(w http.ResponseWriter, r *http.Request) {
	sid, body, ok := a.agentSessionWrite(w, r, "file-ticket", db.MaxProposalArgumentsBytes)
	if !ok {
		return
	}
	row, p, project, ok := a.filingRead(w, r)
	if !ok {
		return
	}
	var req struct {
		Board     string   `json:"board"`
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Type      string   `json:"type"`
		Sensitive bool     `json:"sensitive"`
		Repos     []string `json:"repos"`
	}
	if filingDecode(body, &req) != nil || !filingText(req.Board, 200, false) {
		writeError(w, 400, "Provide a board and a valid ticket draft.")
		return
	}
	boards, err := a.filingBoards(r.Context(), p, project)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	board, err := selectFilingBoard(boards, req.Board)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	draft := boardfiling.Draft{Title: req.Title, Body: req.Body, Type: req.Type, Sensitive: req.Sensitive, Repos: req.Repos}
	if err := validateFilingDraft(draft); err != nil {
		writeFilingError(w, err)
		return
	}
	draft.Origin, err = a.filingOrigin(r.Context(), row)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	up, err := a.filingUpstream()
	if err != nil {
		writeFilingError(w, err)
		return
	}
	draft, err = up.Prepare(r.Context(), p, board.ID, draft)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	raw, _ := json.Marshal(filingArguments{BoardID: board.ID, BoardName: board.Name, Draft: draft})
	if len(raw) > db.MaxProposalArgumentsBytes {
		writeError(w, 413, "The complete filing draft exceeds 64 KiB.")
		return
	}
	proposal, err := db.InsertProposalLimited(r.Context(), a.dbPool, db.Proposal{AccountID: p.AccountID, SessionID: sid, ConnectionID: db.BoardFilingConnectionID, ConnectionName: "board-file", Tool: "file_ticket", ToolHash: "board-file-v1", URLSnapshot: up.SnapshotURL(), Arguments: raw, AgentSummary: "File a ticket after the person's approval."}, db.ProposalLimits{Account: 50, Session: 50, Cron: 50})
	if err != nil {
		writeError(w, 409, "The filing draft could not be queued; check pending proposal limits and retry.")
		return
	}
	// No reproduction text enters events or shared session fanout. Frozen arguments are owner-only.
	writeJSON(w, 202, map[string]any{"proposal_id": proposal.ID, "url": boardBaseURL(os.Getenv("BLERG_RUNNER_SELF_URL")) + "/proposals?id=" + proposal.ID, "state": "pending"})
}

func (h *proposalsHandler) editBoardDraft(w http.ResponseWriter, r *http.Request) {
	who, ok := h.person(w, r)
	if !ok {
		return
	}
	p, ok := h.owned(w, r, who)
	if !ok {
		return
	}
	if !db.IsBoardFilingProposal(p) {
		writeError(w, 400, "Only ticket filing drafts can be edited.")
		return
	}
	var req struct {
		Expected string `json:"expected_arguments_raw"`
		Draft    struct {
			BoardID   string   `json:"board_id"`
			Title     string   `json:"title"`
			Body      string   `json:"body"`
			Type      string   `json:"type"`
			Sensitive bool     `json:"sensitive"`
			Repos     []string `json:"repos"`
		} `json:"draft"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*db.MaxProposalArgumentsBytes))
	if err != nil || filingDecode(raw, &req) != nil || req.Expected == "" {
		writeError(w, 400, "Provide the saved arguments and an edited ticket draft.")
		return
	}
	var old filingArguments
	if json.Unmarshal(p.Arguments, &old) != nil {
		writeError(w, 409, "The saved draft is invalid.")
		return
	}
	proof := mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}
	boards, listErr := h.api.filingBoards(r.Context(), proof, who.Project)
	if listErr != nil {
		writeFilingError(w, listErr)
		return
	}
	board, err := selectFilingBoard(boards, req.Draft.BoardID)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	draft := boardfiling.Draft{Title: req.Draft.Title, Body: req.Draft.Body, Type: req.Draft.Type, Sensitive: req.Draft.Sensitive, Repos: req.Draft.Repos, Origin: old.Origin}
	if err := validateFilingDraft(draft); err != nil {
		writeFilingError(w, err)
		return
	}
	up, err := h.api.filingUpstream()
	if err != nil {
		writeFilingError(w, err)
		return
	}
	if up.SnapshotURL() != p.URLSnapshot {
		writeError(w, 409, "The Board destination changed; propose again.")
		return
	}
	draft, err = up.Prepare(r.Context(), proof, board.ID, draft)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	args, _ := json.Marshal(filingArguments{BoardID: board.ID, BoardName: board.Name, Draft: draft})
	if len(args) > db.MaxProposalArgumentsBytes {
		writeError(w, 413, "The complete draft exceeds 64 KiB.")
		return
	}
	updated, err := db.EditBoardFilingProposal(r.Context(), h.api.dbPool, who.Account, p.ID, req.Expected, args)
	if err != nil {
		transitionError(w, err, "The draft changed, was decided, or expired; reload it before editing.")
		return
	}
	writeJSON(w, 200, viewOfProposal(updated))
}
func (h *proposalsHandler) approveBoardDraft(w http.ResponseWriter, r *http.Request, who grantRequester, p *db.Proposal) {
	var req struct {
		Expected string `json:"expected_arguments_raw"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*db.MaxProposalArgumentsBytes))
	if err != nil || filingDecode(raw, &req) != nil || req.Expected == "" || req.Expected != string(p.Arguments) {
		writeError(w, 409, "The draft changed; reload it and review it before approving.")
		return
	}
	var args filingArguments
	if json.Unmarshal(p.Arguments, &args) != nil || validateFilingDraft(args.Draft) != nil {
		writeError(w, 409, "The saved draft is invalid.")
		return
	}
	proof := mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}
	boards, listErr := h.api.filingBoards(r.Context(), proof, who.Project)
	if listErr != nil {
		writeFilingError(w, listErr)
		return
	}
	if _, err = selectFilingBoard(boards, args.BoardID); err != nil {
		writeFilingError(w, err)
		return
	}
	up, err := h.api.filingUpstream()
	if err != nil {
		writeFilingError(w, err)
		return
	}
	if up.SnapshotURL() != p.URLSnapshot {
		writeError(w, 409, "The Board destination changed; propose again.")
		return
	}
	casCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), proposalWriteBudget)
	defer cancel()
	if _, err = db.BeginBoardFilingExecution(casCtx, h.api.dbPool, who.Account, p.ID, who.Account, req.Expected); err != nil {
		transitionError(w, err, "The draft changed or was already decided.")
		return
	}
	ctx, stop := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer stop()
	result, err := up.Create(ctx, proof, p.ID, args.BoardID, args.Draft)
	recCtx, stopRec := context.WithTimeout(context.WithoutCancel(r.Context()), proposalWriteBudget)
	defer stopRec()
	state := db.ProposalDone
	if err != nil {
		state = db.ProposalUnknown
		if !errors.Is(err, mcpgw.ErrOutcomeUnknown) {
			state = db.ProposalFailed
		}
		var refusal *APIError
		if errors.As(err, &refusal) {
			// A definite preflight/4xx refusal created no card; retain the editable draft.
			if _, releaseErr := db.ReleaseProposal(recCtx, h.api.dbPool, p.ID); releaseErr == nil {
				writeFilingError(w, err)
				return
			}
		}
		result = json.RawMessage(`{"error":"The filing could not be confirmed; check the target before resolving this proposal."}`)
	}
	done, err := db.FinishProposal(recCtx, h.api.dbPool, p.ID, state, result)
	if err != nil {
		writeError(w, 500, "The filing outcome could not be recorded; check the target before resolving this proposal.")
		return
	}
	writeJSON(w, 200, viewOfProposal(done))
}
