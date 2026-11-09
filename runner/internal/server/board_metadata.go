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

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type boardMetadataUpdater interface {
	UpdateMetadata(context.Context, mcpgw.Proof, string, boardfiling.MetadataPatch) (json.RawMessage, error)
}

func (a *API) HandleProposeBoardMetadata(w http.ResponseWriter, r *http.Request) {
	tokenSID, authenticated := a.authMessaging(w, r)
	if !authenticated {
		return
	}
	if tokenSID == "" || tokenSID != r.PathValue("id") {
		writeError(w, http.StatusForbidden, "Use this session's own credential.")
		return
	}
	sid, raw, ok := a.agentSessionWrite(w, r, "board metadata proposals", 64<<10)
	if !ok {
		return
	}
	var req struct {
		Board string `json:"board"`
		boardfiling.MetadataPatch
	}
	if filingDecode(raw, &req) != nil {
		writeError(w, 400, "Provide a board and a metadata patch.")
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil || row == nil || row.Kind != "agent" || row.SpawningAccountID == nil {
		writeError(w, 409, "This session cannot propose board metadata.")
		return
	}
	proof, project, err := a.filingProof(r.Context(), row)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	boards, err := a.sessionFilingBoards(r.Context(), row, proof, project)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	board, err := selectFilingBoard(boards, req.Board)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	lv, err := a.effectiveBoardLevel(r.Context(), row, board.ID)
	if err != nil || db.BoardLevelRank(lv.Level) < db.BoardLevelRank(db.BoardLevelRead) {
		writeError(w, 403, "Board metadata proposals need Read access or higher.")
		return
	}
	d := req.MetadataPatch
	d.BoardID = board.ID
	if err = boardfiling.ValidateMetadataPatch(d); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	up, err := a.filingUpstream()
	if err != nil {
		writeFilingError(w, err)
		return
	}
	args, _ := json.Marshal(d)
	p, err := db.InsertProposalLimited(r.Context(), a.dbPool, db.Proposal{AccountID: *row.SpawningAccountID, SessionID: sid, ConnectionID: db.BoardMetadataConnectionID, ConnectionName: "Board metadata", Tool: "update_board_metadata", ToolHash: "board-metadata-v1", URLSnapshot: up.SnapshotURL(), Arguments: args, AgentSummary: "Update board metadata after your approval."}, db.ProposalLimits{Account: 50, Session: 20, Cron: 20})
	if err != nil {
		writeError(w, 409, "The proposal could not be queued; check pending proposal limits.")
		return
	}
	if !p.Repeated {
		a.announceProposalRequest(r.Context(), p, "")
	}
	writeJSON(w, 202, map[string]any{"proposal_id": p.ID, "state": p.State, "url": boardBaseURL(os.Getenv("BLERG_RUNNER_SELF_URL")) + "/proposals?id=" + p.ID})
}
func (h *proposalsHandler) approveBoardMetadata(ctx context.Context, w http.ResponseWriter, who grantRequester, p *db.Proposal, raw []byte) {
	var body approveBody
	if len(bytes.TrimSpace(raw)) > 0 && filingDecode(raw, &body) != nil {
		writeError(w, 400, "Invalid approval request.")
		return
	}
	if body.AllowForSession {
		writeError(w, 422, "Board metadata changes always need your approval.")
		return
	}
	if body.Expected != "" && body.Expected != string(p.Arguments) {
		writeError(w, 409, "The proposal changed; review it again.")
		return
	}
	var d boardfiling.MetadataPatch
	if filingDecode(p.Arguments, &d) != nil || boardfiling.ValidateMetadataPatch(d) != nil {
		writeError(w, 409, "The saved patch is invalid; reject it.")
		return
	}
	if who.Project != "" && who.Project != d.BoardID {
		writeError(w, 403, "This sign-in is confined to another board.")
		return
	}
	row, err := db.GetSession(ctx, h.api.dbPool, p.SessionID)
	if err != nil || row == nil || !ownsSession(who.Account, row) {
		writeError(w, 409, "The proposing session is gone.")
		return
	}
	lv, err := h.api.effectiveBoardLevel(ctx, row, d.BoardID)
	if err != nil || db.BoardLevelRank(lv.Level) < db.BoardLevelRank(db.BoardLevelRead) {
		writeError(w, 403, "The session no longer has Read access to this board.")
		return
	}
	up, err := h.api.filingUpstream()
	if err != nil {
		writeFilingError(w, err)
		return
	}
	updater, ok := up.(boardMetadataUpdater)
	if !ok {
		writeError(w, 503, "Board metadata approval is unavailable.")
		return
	}
	if up.SnapshotURL() != p.URLSnapshot {
		writeError(w, 409, "The Board destination changed; propose again.")
		return
	}
	proof := mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}
	boards, err := h.api.filingBoards(ctx, proof, who.Project)
	if err != nil {
		writeFilingError(w, err)
		return
	}
	if _, err = selectFilingBoard(boards, d.BoardID); err != nil {
		writeFilingError(w, err)
		return
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err = db.BeginBoardMetadataExecution(runCtx, h.api.dbPool, who.Account, p.ID, string(p.Arguments)); err != nil {
		transitionError(w, err, "The proposal was already decided or expired.")
		return
	}
	result, err := updater.UpdateMetadata(runCtx, proof, p.ID, d)
	recCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), proposalWriteBudget)
	defer stop()
	state := db.ProposalDone
	if err != nil {
		var refusal *APIError
		if errors.As(err, &refusal) {
			if _, releaseErr := db.ReleaseProposal(recCtx, h.api.dbPool, p.ID); releaseErr == nil {
				writeFilingError(w, err)
				return
			}
		}
		state = db.ProposalUnknown
		result = json.RawMessage(`{"error":"The change could not be confirmed; check Board before resolving this proposal."}`)
	}
	done, err := db.FinishProposal(recCtx, h.api.dbPool, p.ID, state, result)
	if err != nil {
		writeError(w, 500, "The outcome could not be recorded; check Board.")
		return
	}
	writeJSON(w, 200, viewOfProposal(done))
}
func (c *httpBoardFiling) UpdateMetadata(ctx context.Context, proof mcpgw.Proof, intent string, d boardfiling.MetadataPatch) (json.RawMessage, error) {
	if proof.AccountID == "" || proof.SessionID == "" || proof.TokenID != "" {
		return nil, apiErrorf(403, "A signed-in person must approve this change.")
	}
	raw, _ := json.Marshal(map[string]string{"account_id": proof.AccountID, "session_id": proof.SessionID, "board_id": d.BoardID, "intent": intent, "digest": boardfiling.MetadataDigest(d)})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.core.BaseURL, "/")+"/internal/tokens/exchange/board-metadata", bytes.NewReader(raw))
	if err != nil {
		return nil, apiErrorf(503, "Board approval is unavailable.")
	}
	req.Header.Set("X-Internal-Key", c.core.InternalKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.core.httpClient().Do(req)
	if err != nil {
		return nil, apiErrorf(503, "Board approval could not verify your access.")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, apiErrorf(resp.StatusCode, "Board metadata approval needs a live administrator sign-in and an updated Core.")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<10)+1))
	var tok struct {
		Token string `json:"token"`
	}
	if err != nil || len(data) > 16<<10 || json.Unmarshal(data, &tok) != nil || tok.Token == "" || len(tok.Token) > 8<<10 || strings.ContainsAny(tok.Token, "\r\n \t") {
		return nil, apiErrorf(503, "Board approval credential unavailable.")
	}
	receipt, err := c.boardRequest(ctx, "POST", "/api/boards/"+d.BoardID+"/metadata-approved", tok.Token, d)
	if err != nil {
		return nil, err
	}
	var confirmed struct {
		BoardID string `json:"board_id"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(receipt, &confirmed) != nil || confirmed.BoardID != d.BoardID || confirmed.Status != "updated" {
		return nil, mcpgw.ErrOutcomeUnknown
	}
	return receipt, nil
}
