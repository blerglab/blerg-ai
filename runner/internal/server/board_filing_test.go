package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type fixtureFiling struct {
	mu      sync.Mutex
	creates int
	draft   boardfiling.Draft
	board   string
	err     error
	receipt json.RawMessage // what Create answers; empty: the legacy fixture receipt
	proofs  []mcpgw.Proof   // the proof of every Create
}

func (f *fixtureFiling) SnapshotURL() string { return "https://board.example.test" }
func (f *fixtureFiling) Prepare(_ context.Context, _ mcpgw.Proof, _ string, d boardfiling.Draft) (boardfiling.Draft, error) {
	return d, nil
}
func (f *fixtureFiling) Search(context.Context, mcpgw.Proof, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{"cards":[],"truncated":false}`), nil
}
func (f *fixtureFiling) Create(_ context.Context, p mcpgw.Proof, _ string, b string, d boardfiling.Draft) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	f.draft = d
	f.board = b
	f.proofs = append(f.proofs, p)
	if len(f.receipt) > 0 {
		return f.receipt, f.err
	}
	return json.RawMessage(`{"id":"fixture-card","title":"Approved"}`), f.err
}

type filingBoardList struct{ *fakeMCPList }

func (*filingBoardList) ListSessionBoards(context.Context, mcpgw.Proof) ([]SessionBoard, error) {
	return []SessionBoard{{ID: "board-a", Name: "A"}, {ID: "board-b", Name: "B"}}, nil
}
func newFilingFx(t *testing.T) (*topicFx, *fixtureFiling) {
	fx := newWorkFx(t)
	fx.api.RegisterProposalRoutes(fx.mux)
	up := &fixtureFiling{}
	fx.api.boardFiling = up
	fx.api.sessionOriginLookup = func(context.Context, string) (string, error) { return strings.Repeat("a", 40), nil }
	t.Setenv("BLERG_RUNNER_SELF_URL", "https://runner.example.test")
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}})
	if err := db.SaveBoardFilingProof(t.Context(), fx.pool, fx.sess, "session_id", "fixture-login", ""); err != nil {
		t.Fatal(err)
	}
	return fx, up
}
func filingRequest(fx *topicFx, method, path, token string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}
func queueFiling(t *testing.T, fx *topicFx) *db.Proposal {
	t.Helper()
	code, out := fx.postWork("file-ticket", fx.sess, fx.sessTok, map[string]any{"board": "A", "title": "Original", "body": "Private reproduction fixture", "sensitive": true})
	if code != 202 {
		t.Fatalf("queue code=%d", code)
	}
	id, _ := out["proposal_id"].(string)
	p, err := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestBoardFilingQueuesOnlyAndApprovalUsesSavedDraft(t *testing.T) {
	fx, up := newFilingFx(t)
	p := queueFiling(t, fx)
	if up.creates != 0 {
		t.Fatal("board write happened before approval")
	}
	// The chat's card is announced by id and kind only: no draft text enters shared session fanout.
	for len(fx.events) > 0 {
		ev := string(<-fx.events)
		if strings.Contains(ev, "Private reproduction fixture") || strings.Contains(ev, "Original") || !strings.Contains(ev, `"proposal_request"`) {
			t.Fatalf("private draft entered shared session fanout: %s", ev)
		}
	}
	var args filingArguments
	if json.Unmarshal(p.Arguments, &args) != nil || args.Origin.Repo != "app" || args.Origin.Commit != strings.Repeat("a", 40) || args.Origin.Engine != "claude" || args.Origin.SessionURL != "https://runner.example.test/sessions/"+fx.sess {
		t.Fatal("structured origin missing")
	}
	human := fx.token(topicOwner, "human")
	edit := filingRequest(fx, "PATCH", "/api/proposals/"+p.ID+"/draft", human, map[string]any{"expected_arguments_raw": string(p.Arguments), "draft": map[string]any{"board_id": "board-b", "title": "Approved edit", "body": "Edited private fixture", "type": "bug", "sensitive": true, "repos": []string{"team/target"}}})
	if edit.Code != 200 {
		t.Fatalf("edit code=%d body=%s", edit.Code, edit.Body.String())
	}
	edited, err := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/approve", human, map[string]any{"expected_arguments_raw": string(p.Arguments)})
	if stale.Code != 409 || up.creates != 0 {
		t.Fatal("stale draft approved")
	}
	approved := filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/approve", human, map[string]any{"expected_arguments_raw": string(edited.Arguments)})
	if approved.Code != 200 || up.creates != 1 || up.board != "board-b" || up.draft.Title != "Approved edit" || up.draft.Body != "Edited private fixture" || !up.draft.Sensitive || len(up.draft.Repos) != 1 || up.draft.Repos[0] != "team/target" || up.draft.Origin != args.Origin {
		t.Fatalf("saved edit not honored: code=%d", approved.Code)
	}
}
func TestBoardFilingConcurrentApprovalExecutesOnce(t *testing.T) {
	fx, up := newFilingFx(t)
	p := queueFiling(t, fx)
	human := fx.token(topicOwner, "human")
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			codes <- filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/approve", human, map[string]any{"expected_arguments_raw": string(p.Arguments)}).Code
		})
	}
	wg.Wait()
	close(codes)
	wins := 0
	for code := range codes {
		if code == 200 {
			wins++
		} else if code != 409 {
			t.Fatalf("approval=%d", code)
		}
	}
	if wins != 1 || up.creates != 1 {
		t.Fatalf("approval winners=%d writes=%d", wins, up.creates)
	}
}
func TestBoardFilingRefusesUnauthorizedAndSpoofedDrafts(t *testing.T) {
	fx, up := newFilingFx(t)
	p := queueFiling(t, fx)
	for _, path := range []string{"/api/proposals/" + p.ID, "/api/proposals/" + p.ID + "/approve", "/api/proposals/" + p.ID + "/draft"} {
		method := "GET"
		if strings.HasSuffix(path, "approve") {
			method = "POST"
		}
		if strings.HasSuffix(path, "draft") {
			method = "PATCH"
		}
		rec := filingRequest(fx, method, path, fx.token(topicOther, "human"), map[string]any{"expected_arguments_raw": string(p.Arguments)})
		if rec.Code != 404 {
			t.Fatalf("non-owner %s=%d", method, rec.Code)
		}
	}
	for _, body := range []any{map[string]any{"board": "A", "title": "x", "origin": map[string]string{"engine": "spoof"}}, map[string]any{"board": "A", "title": strings.Repeat("x", 201)}, map[string]any{"board": "unknown", "title": "x"}, map[string]any{"board": "A", "title": "x", "body": "\x00"}, map[string]any{"board": "A", "title": "x", "repos": []string{""}}} {
		code, _ := fx.postWork("file-ticket", fx.sess, fx.sessTok, body)
		if code != 400 && code != 404 {
			t.Fatalf("spoofed/invalid draft=%d", code)
		}
	}
	code, _ := fx.postWork("file-ticket", fx.sess, fx.otherTok, map[string]any{"board": "A", "title": "x"})
	if code != 403 {
		t.Fatalf("other session=%d", code)
	}
	if up.creates != 0 {
		t.Fatal("refusal wrote a card")
	}
}
func TestBoardFilingRejectExpiryUnknownAndDefiniteRefusal(t *testing.T) {
	for _, kind := range []string{"reject", "expired", "unknown", "definite"} {
		t.Run(kind, func(t *testing.T) {
			fx, up := newFilingFx(t)
			p := queueFiling(t, fx)
			human := fx.token(topicOwner, "human")
			path := "/api/proposals/" + p.ID + "/approve"
			wantState := db.ProposalDone
			wantCode := 200
			switch kind {
			case "reject":
				path = "/api/proposals/" + p.ID + "/reject"
				wantState = db.ProposalRejected
			case "expired":
				_, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET created_at=now()-interval '8 days' WHERE id=$1`, p.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantState = db.ProposalPending
				wantCode = 409
			case "unknown":
				up.err = mcpgw.ErrOutcomeUnknown
				wantState = db.ProposalUnknown
			case "definite":
				up.err = apiErrorf(422, "Choose a target repository.")
				wantState = db.ProposalPending
				wantCode = 422
			}
			rec := filingRequest(fx, "POST", path, human, map[string]any{"expected_arguments_raw": string(p.Arguments)})
			if rec.Code != wantCode {
				t.Fatalf("%s code=%d", kind, rec.Code)
			}
			saved, err := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, p.ID)
			if err != nil || saved.State != wantState {
				t.Fatalf("%s state=%v err=%v", kind, saved, err)
			}
			if (kind == "reject" || kind == "expired") && up.creates != 0 {
				t.Fatal("unapproved card created")
			}
		})
	}
}
func TestBoardFilingMissingProofOrOriginCannotQueue(t *testing.T) {
	for _, kind := range []string{"proof", "origin", "public-url"} {
		t.Run(kind, func(t *testing.T) {
			fx, up := newFilingFx(t)
			switch kind {
			case "proof":
				_, err := fx.pool.Exec(t.Context(), `DELETE FROM session_board_filing_proof WHERE session_id=$1`, fx.sess)
				if err != nil {
					t.Fatal(err)
				}
			case "origin":
				fx.api.sessionOriginLookup = func(context.Context, string) (string, error) { return "", errors.New("fixture offline") }
			case "public-url":
				t.Setenv("BLERG_RUNNER_SELF_URL", "")
			}
			code, _ := fx.postWork("file-ticket", fx.sess, fx.sessTok, map[string]any{"board": "A", "title": "x"})
			if code != 409 && code != 503 {
				t.Fatalf("missing %s accepted: %d", kind, code)
			}
			n, err := db.CountPendingProposals(t.Context(), fx.pool, topicOwner)
			if err != nil || n != 0 || up.creates != 0 {
				t.Fatal("incomplete origin created proposal or card")
			}
		})
	}
}

func TestBoardFilingScopeAndTransportParity(t *testing.T) {
	fx, up := newFilingFx(t)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE session_board_filing_proof SET project_scope='board-a' WHERE session_id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	rec := filingRequest(fx, "GET", "/api/sessions/"+fx.sess+"/board-options", fx.sessTok, nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "board-b") {
		t.Fatal("discovery escaped launch project")
	}
	code, _ := fx.postWork("file-ticket", fx.sess, fx.sessTok, map[string]any{"board": "B", "title": "x"})
	if code != 404 {
		t.Fatalf("project escape=%d", code)
	}
	p := queueFiling(t, fx)
	human := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: topicOwner, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Project: "board-a", Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	rec = filingRequest(fx, "PATCH", "/api/proposals/"+p.ID+"/draft", human, map[string]any{"expected_arguments_raw": string(p.Arguments), "draft": map[string]any{"board_id": "board-b", "title": "Escaped", "body": "x"}})
	if rec.Code != 404 {
		t.Fatalf("approver project escape=%d", rec.Code)
	}
	for _, action := range []string{"list_boards", "search", "propose"} {
		args := map[string]any{"action": action, "board": "board-a", "query": "duplicate", "title": "MCP draft", "body": "private"}
		rec = filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/filing-tools", fx.sessTok, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "blerg_ticket_file", "arguments": args}})
		if rec.Code != 200 || strings.Contains(rec.Body.String(), `"isError":true`) {
			t.Fatalf("MCP %s failed: %s", action, rec.Body.String())
		}
	}
	if up.creates != 0 {
		t.Fatal("MCP propose wrote to Board")
	}
	rec = filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/filing-tools", fx.otherTok, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if rec.Code != 403 {
		t.Fatal("MCP accepted another session token")
	}
	rec = filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/filing-tools", fx.sessTok, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "blerg_card_create", "arguments": map[string]any{}}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatal("unlisted MCP tool accepted")
	}
}
func TestBoardFilingViewerProofBootstrapIsOwnerOnly(t *testing.T) {
	fx := newWorkFx(t)
	if _, err := fx.pool.Exec(t.Context(), `DELETE FROM session_board_filing_proof WHERE session_id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if rec := fx.getLive(fx.sess, fx.token(topicOther, "human"), ""); rec.Code != 200 {
		t.Fatalf("public view=%d", rec.Code)
	}
	if _, _, _, err := db.BoardFilingProof(t.Context(), fx.pool, fx.sess); err == nil {
		t.Fatal("foreign viewer delegated a proof")
	}
	if rec := fx.getLive(fx.sess, fx.token(topicOwner, "human"), ""); rec.Code != 200 {
		t.Fatalf("owner view=%d", rec.Code)
	}
	kind, value, _, err := db.BoardFilingProof(t.Context(), fx.pool, fx.sess)
	if err != nil || kind != "session_id" || value != testSID {
		t.Fatal("owner proof not retained for legacy session")
	}
}
func TestBoardFilingNormalMCPProposalCannotBeEdited(t *testing.T) {
	fx, _ := newFilingFx(t)
	p, err := db.InsertProposal(t.Context(), fx.pool, db.Proposal{AccountID: topicOwner, ConnectionID: newUUID(), ConnectionName: "mail", URLSnapshot: "https://mcp.example.test", Tool: "send", ToolHash: "hash", Arguments: json.RawMessage(`{"body":"original"}`)}, 50)
	if err != nil {
		t.Fatal(err)
	}
	rec := filingRequest(fx, "PATCH", "/api/proposals/"+p.ID+"/draft", fx.token(topicOwner, "human"), map[string]any{"expected_arguments_raw": string(p.Arguments), "draft": map[string]any{"board_id": "board-a", "title": "not a card"}})
	if rec.Code != 400 {
		t.Fatalf("ordinary MCP edited=%d", rec.Code)
	}
}
