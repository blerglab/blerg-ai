package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// fixtureBoardCreator is the board upstream that also creates approved boards. Each created board
// is "board-a", which the fixture's discovery (filingBoardList) lists for the owner.
type fixtureBoardCreator struct {
	*fixtureFiling
	mu      sync.Mutex
	created []boardfiling.BoardDraft
	proofs  []mcpgw.Proof
	err     error
}

func (f *fixtureBoardCreator) CreateBoard(_ context.Context, p mcpgw.Proof, _ string, d boardfiling.BoardDraft) (createdBoard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return createdBoard{}, f.err
	}
	f.created = append(f.created, d)
	f.proofs = append(f.proofs, p)
	return createdBoard{ID: "board-a", Name: d.Name, Repos: d.Repos}, nil
}

func (f *fixtureBoardCreator) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func newBoardCreateFx(t *testing.T) (*topicFx, *fixtureBoardCreator) {
	t.Helper()
	fx, up := newFilingFx(t)
	c := &fixtureBoardCreator{fixtureFiling: up}
	fx.api.boardFiling = c
	return fx, c
}

func proposeBoard(fx *topicFx, sid, token, key string, body any) (int, map[string]any) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sid+"/board-create", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func queueBoard(t *testing.T, fx *topicFx, sid, token string, body map[string]any) *db.Proposal {
	t.Helper()
	code, out := proposeBoard(fx, sid, token, "", body)
	if code != http.StatusAccepted {
		t.Fatalf("propose = %d %v", code, out)
	}
	id, _ := out["proposal_id"].(string)
	p, err := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func approveBoard(fx *topicFx, token string, p *db.Proposal, extra map[string]any) *httptest.ResponseRecorder {
	body := map[string]any{"expected_arguments_raw": string(p.Arguments)}
	for k, v := range extra {
		body[k] = v
	}
	return filingRequest(fx, http.MethodPost, "/api/proposals/"+p.ID+"/approve", token, body)
}

func levelOf(t *testing.T, fx *topicFx, sid, board string) string {
	t.Helper()
	row, err := db.GetSession(t.Context(), fx.pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	lv, err := fx.api.effectiveBoardLevel(t.Context(), row, board)
	if err != nil {
		t.Fatal(err)
	}
	return lv.Level
}

func TestBoardProposalApprovedCreatesTheBoardAndGivesTheSessionAccess(t *testing.T) {
	fx, c := newBoardCreateFx(t)
	p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch", "repos": []string{"team/app", "web"}, "columns": []string{"Todo", "Done"}, "access": "file"})
	if c.count() != 0 || !db.IsBoardCreateProposal(p) || p.State != db.ProposalPending {
		t.Fatal("a board was created before the person approved")
	}
	// The chat names the proposal and its kind only.
	sawRequest := false
	for len(fx.events) > 0 {
		ev := string(<-fx.events)
		if strings.Contains(ev, "Launch") || strings.Contains(ev, "team/app") {
			t.Fatalf("draft text entered shared session fanout: %s", ev)
		}
		sawRequest = sawRequest || strings.Contains(ev, `"board_create"`)
	}
	if !sawRequest {
		t.Fatal("no proposal card in the chat")
	}
	human := fx.token(topicOwner, "human")
	view := filingRequest(fx, http.MethodGet, "/api/proposals/"+p.ID, human, nil)
	if view.Code != 200 || !strings.Contains(view.Body.String(), `"kind":"board_create"`) {
		t.Fatalf("view = %d %s", view.Code, view.Body.String())
	}
	if lv := levelOf(t, fx, fx.sess, "board-a"); lv != db.BoardLevelPropose {
		t.Fatalf("level before approval = %s", lv)
	}
	rec := approveBoard(fx, human, p, nil)
	if rec.Code != 200 || c.count() != 1 {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body.String())
	}
	d := c.created[0]
	if d.Name != "Launch" || len(d.Repos) != 2 || d.Repos[0] != "team/app" || len(d.Columns) != 2 || c.proofs[0].AccountID != topicOwner || c.proofs[0].SessionID != testSID {
		t.Fatalf("created %+v with %+v", d, c.proofs[0])
	}
	if lv := levelOf(t, fx, fx.sess, "board-a"); lv != db.BoardLevelFile {
		t.Fatalf("session level = %s, want file", lv)
	}
	done, _ := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, p.ID)
	b := readCreatedBoard(done)
	if done.State != db.ProposalDone || b == nil || b.ID != "board-a" || b.Access != db.BoardLevelFile || b.Requested != db.BoardLevelFile {
		t.Fatalf("result %s %s", done.State, done.Result)
	}
	// The session reads the outcome; the decision message names the board and the access.
	st := filingRequest(fx, http.MethodGet, "/api/sessions/"+fx.sess+"/proposals/"+p.ID, fx.sessTok, nil)
	if st.Code != 200 || !strings.Contains(st.Body.String(), `"board_id":"board-a"`) || !strings.Contains(st.Body.String(), "your access to it: File without asking") {
		t.Fatalf("status = %d %s", st.Code, st.Body.String())
	}
	// Approving again is refused and creates nothing more.
	if again := approveBoard(fx, human, p, nil); again.Code != http.StatusConflict || c.count() != 1 {
		t.Fatalf("second approval = %d, boards %d", again.Code, c.count())
	}
}

func TestBoardProposalConcurrentApprovalsCreateOne(t *testing.T) {
	fx, c := newBoardCreateFx(t)
	p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch"})
	human := fx.token(topicOwner, "human")
	var wg sync.WaitGroup
	codes := make(chan int, 6)
	for range 6 {
		wg.Go(func() { codes <- approveBoard(fx, human, p, nil).Code })
	}
	wg.Wait()
	close(codes)
	wins := 0
	for code := range codes {
		if code == 200 {
			wins++
		} else if code != http.StatusConflict {
			t.Fatalf("approval = %d", code)
		}
	}
	if wins != 1 || c.count() != 1 {
		t.Fatalf("winners %d, boards %d", wins, c.count())
	}
}

func TestBoardProposalPersonMayLowerButNotRaiseAccess(t *testing.T) {
	fx, c := newBoardCreateFx(t)
	human := fx.token(topicOwner, "human")
	p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch", "access": "read"})
	if rec := approveBoard(fx, human, p, map[string]any{"access": "full"}); rec.Code != http.StatusBadRequest || c.count() != 0 {
		t.Fatalf("raise = %d", rec.Code)
	}
	p2 := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Second"})
	if rec := approveBoard(fx, human, p2, map[string]any{"access": "read"}); rec.Code != 200 {
		t.Fatalf("lower = %d %s", rec.Code, rec.Body.String())
	}
	if lv := levelOf(t, fx, fx.sess, "board-a"); lv != db.BoardLevelRead {
		t.Fatalf("level = %s, want read", lv)
	}
}

func TestBoardProposalRejectedOrRefusedCreatesNothing(t *testing.T) {
	fx, c := newBoardCreateFx(t)
	human := fx.token(topicOwner, "human")
	p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch"})
	if rec := filingRequest(fx, http.MethodPost, "/api/proposals/"+p.ID+"/reject", human, map[string]any{"note": "Not now"}); rec.Code != 200 {
		t.Fatalf("reject = %d", rec.Code)
	}
	if rec := approveBoard(fx, human, p, nil); rec.Code != http.StatusConflict {
		t.Fatalf("approve after reject = %d", rec.Code)
	}
	st := filingRequest(fx, http.MethodGet, "/api/sessions/"+fx.sess+"/proposals/"+p.ID, fx.sessTok, nil)
	if !strings.Contains(st.Body.String(), "No board was created") {
		t.Fatalf("status %s", st.Body.String())
	}
	// A definite refusal (not an administrator) creates nothing and the proposal waits again.
	c.err = apiErrorf(http.StatusForbidden, "Only an administrator can create boards: ask one to approve it, or reject it.")
	p2 := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Other"})
	if rec := approveBoard(fx, human, p2, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "administrator") {
		t.Fatalf("refused approve = %d %s", rec.Code, rec.Body.String())
	}
	again, _ := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, p2.ID)
	if again.State != db.ProposalPending || c.count() != 0 || levelOf(t, fx, fx.sess, "board-a") != db.BoardLevelPropose {
		t.Fatalf("refusal left state %s, boards %d", again.State, c.count())
	}
}

func TestBoardProposalOnlyTheOwnerDecides(t *testing.T) {
	fx, c := newBoardCreateFx(t)
	p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch"})
	// Another person sees nothing; the session's own token, an agent token and the runner key cannot decide.
	for _, tok := range []string{fx.token(topicOther, "human"), fx.sessTok, fx.token(topicOwner, "agent"), topicDaemonToken} {
		if rec := approveBoard(fx, tok, p, nil); rec.Code < 400 {
			t.Fatalf("approve by %q... = %d", tok[:8], rec.Code)
		}
	}
	// "Allow for this session" never applies to a proposed board.
	if rec := approveBoard(fx, fx.token(topicOwner, "human"), p, map[string]any{"allow_for_session": true}); rec.Code < 400 {
		t.Fatalf("allow for session = %d", rec.Code)
	}
	if c.count() != 0 {
		t.Fatal("a board was created without the owner's approval")
	}
	// A different session cannot propose in this session's name.
	if code, _ := proposeBoard(fx, fx.sess, fx.otherTok, "", map[string]any{"name": "Spoof"}); code != http.StatusForbidden {
		t.Fatalf("other session = %d", code)
	}
}

func TestBoardProposalValidatesTheDraft(t *testing.T) {
	fx, c := newBoardCreateFx(t)
	for _, body := range []any{
		map[string]any{"name": ""}, map[string]any{"name": "a\nb"}, map[string]any{"name": "x", "repos": []string{"../etc"}},
		map[string]any{"name": "x", "repos": []string{"a/b/c"}}, map[string]any{"name": "x", "access": "admin"},
		map[string]any{"name": "x", "access": "propose"}, map[string]any{"name": "x", "template": "focus-board", "columns": []string{"a"}},
		map[string]any{"name": "x", "board_admin": true}, map[string]any{"name": "x", "description": "a\x00b"},
	} {
		if code, out := proposeBoard(fx, fx.sess, fx.sessTok, "", body); code != http.StatusBadRequest {
			t.Fatalf("%v = %d %v", body, code, out)
		}
	}
	if code, _ := proposeBoard(fx, fx.sess, fx.sessTok, "bad key!", map[string]any{"name": "x"}); code != http.StatusBadRequest {
		t.Fatalf("bad key = %d", code)
	}
	if c.count() != 0 {
		t.Fatal("invalid draft created a board")
	}
}

func TestBoardProposalIdempotency(t *testing.T) {
	fx, _ := newBoardCreateFx(t)
	body := map[string]any{"name": "Launch", "repos": []string{"team/app"}}
	code, first := proposeBoard(fx, fx.sess, fx.sessTok, "key-1", body)
	if code != http.StatusAccepted {
		t.Fatalf("first = %d", code)
	}
	code, again := proposeBoard(fx, fx.sess, fx.sessTok, "key-1", body)
	if code != http.StatusAccepted || again["proposal_id"] != first["proposal_id"] || again["repeated"] != true {
		t.Fatalf("repeat = %d %v", code, again)
	}
	if code, _ := proposeBoard(fx, fx.sess, fx.sessTok, "key-1", map[string]any{"name": "Other"}); code != http.StatusConflict {
		t.Fatalf("reused key, other draft = %d", code)
	}
	// Without a key, the same draft soon after is the same proposal; a new key is a new one.
	_, a := proposeBoard(fx, fx.sess, fx.sessTok, "", map[string]any{"name": "Plain"})
	_, b := proposeBoard(fx, fx.sess, fx.sessTok, "", map[string]any{"name": "Plain"})
	_, c := proposeBoard(fx, fx.sess, fx.sessTok, "key-00000002", map[string]any{"name": "Launch", "repos": []string{"team/app"}})
	if a["proposal_id"] != b["proposal_id"] || c["proposal_id"] == first["proposal_id"] {
		t.Fatalf("dedup: %v %v %v", a, b, c)
	}
}

func TestBoardProposalFromAStartedSessionFollowsItsParentsCap(t *testing.T) {
	fx, c := newBoardCreateFx(t)
	child := newChildOf(t, fx)
	p := queueBoard(t, fx, child.id, child.tok, map[string]any{"name": "Child board"})
	rec := approveBoard(fx, fx.token(topicOwner, "human"), p, nil)
	if rec.Code != 200 || c.count() != 1 {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body.String())
	}
	done, _ := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, p.ID)
	b := readCreatedBoard(done)
	// The parent has no access to the new board, so the child gets none either.
	if b == nil || b.Access != db.BoardLevelPropose || !strings.Contains(b.AccessNote, "started this one") || levelOf(t, fx, child.id, "board-a") != db.BoardLevelPropose {
		t.Fatalf("child result %s", done.Result)
	}
}

func TestBoardProposalRefusedForSessionsThatCannotHoldAccess(t *testing.T) {
	fx, c := newBoardCreateFx(t)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET restrict_tools = true WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if code, out := proposeBoard(fx, fx.sess, fx.sessTok, "", map[string]any{"name": "Launch"}); code != http.StatusConflict || !strings.Contains(out["error"].(string), "Board app") {
		t.Fatalf("restricted = %d %v", code, out)
	}
	if c.count() != 0 {
		t.Fatal("created")
	}
}

func TestBoardDecisionMessages(t *testing.T) {
	result, _ := json.Marshal(createdBoard{ID: "b1", Name: "Launch", Access: db.BoardLevelFull, Requested: db.BoardLevelFull})
	p := &db.Proposal{ID: "p1", ConnectionID: db.BoardCreateConnectionID, ConnectionName: "board-create", Tool: "create_board", ToolHash: "board-create-v1", State: db.ProposalDone, Result: result}
	if m := proposalDecisionMessage(p); !strings.Contains(m, `board "Launch" was created (board id b1); your access to it: Full`) {
		t.Fatal(m)
	}
	p.State, p.Result = db.ProposalExpired, nil
	if m := proposalDecisionMessage(p); !strings.Contains(m, "no board was created") {
		t.Fatal(m)
	}
}

func TestBoardCreateHTTPBindsTheApprovedDraft(t *testing.T) {
	draft := boardfiling.BoardDraft{Name: "Launch", Repos: []string{"team/app"}, Template: "focus-board"}
	intent := "66666666-6666-4666-8666-666666666666"
	for _, mode := range []string{"ok", "not-admin", "signed-out", "old-core", "board-refused", "wrong-receipt", "board-clock", "board-used", "old-board"} {
		t.Run(mode, func(t *testing.T) {
			board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/boards/create-approved" || r.Header.Get("Authorization") != "Bearer fixture-board-grant" {
					t.Error("create changed trusted route or credential")
				}
				var got boardfiling.BoardDraft
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || boardfiling.BoardDigest(got) != boardfiling.BoardDigest(draft) {
					t.Error("create mutated the approved draft")
				}
				switch mode {
				case "board-refused":
					w.WriteHeader(http.StatusBadRequest)
					return
				case "wrong-receipt":
					_, _ = w.Write([]byte(`{"id":"` + filingHTTPBoard + `","name":"Other"}`))
					return
				case "board-clock":
					w.WriteHeader(http.StatusUnauthorized)
					return
				case "board-used":
					w.WriteHeader(http.StatusConflict)
					return
				case "old-board":
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"` + filingHTTPBoard + `","name":"Launch","repos":["team/app"]}`))
			}))
			defer board.Close()
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var got map[string]string
				_ = json.NewDecoder(r.Body).Decode(&got)
				if r.URL.Path != "/internal/tokens/exchange/board-new" || r.Header.Get("X-Internal-Key") != "fixture-internal" || got["account_id"] != filingHTTPProof.AccountID ||
					got["session_id"] != filingHTTPProof.SessionID || got["intent"] != intent || got["digest"] != boardfiling.BoardDigest(draft) || len(got) != 4 {
					t.Errorf("exchange request %s %v", r.URL.Path, got)
				}
				switch mode {
				case "not-admin":
					w.WriteHeader(http.StatusForbidden)
					return
				case "signed-out":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":"not_live"}`))
					return
				case "old-core":
					http.NotFound(w, r) // a core without the route: the mux's plain-text 404
					return
				}
				_, _ = w.Write([]byte(`{"token":"fixture-board-grant"}`))
			}))
			defer core.Close()
			up := &httpBoardFiling{base: board.URL, core: &HTTPMCPCoreClient{BaseURL: core.URL, InternalKey: "fixture-internal", HTTP: core.Client()}}
			got, err := up.CreateBoard(t.Context(), filingHTTPProof, intent, draft)
			var apiErr *APIError
			switch mode {
			case "ok":
				if err != nil || got.ID != filingHTTPBoard || got.Name != "Launch" || len(got.Repos) != 1 {
					t.Fatalf("created %+v %v", got, err)
				}
			case "not-admin":
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || !strings.Contains(apiErr.Message, "administrator") {
					t.Fatalf("not admin: %v", err)
				}
			case "signed-out":
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
					t.Fatalf("signed out: %v", err)
				}
			case "old-core":
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable || apiErr.Message != msgCoreTooOld {
					t.Fatalf("old core: %v", err)
				}
			case "board-clock":
				if !errors.As(err, &apiErr) || apiErr.Message != msgBoardClock {
					t.Fatalf("board clock: %v", err)
				}
			case "board-used":
				if !errors.Is(err, errBoardApprovalUsed) {
					t.Fatalf("board used: %v", err)
				}
			case "old-board":
				if !errors.As(err, &apiErr) || apiErr.Message != msgBoardTooOld {
					t.Fatalf("old board: %v", err)
				}
			case "board-refused":
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
					t.Fatalf("refused: %v", err)
				}
			case "wrong-receipt":
				if !errors.Is(err, mcpgw.ErrOutcomeUnknown) {
					t.Fatalf("wrong receipt: %v", err)
				}
			}
		})
	}
	// Only a signed-in person's proof is accepted.
	up := &httpBoardFiling{base: "http://127.0.0.1:1", core: &HTTPMCPCoreClient{BaseURL: "http://127.0.0.1:1", InternalKey: "k"}}
	if _, err := up.CreateBoard(t.Context(), mcpgw.Proof{AccountID: "a", TokenID: "t"}, intent, draft); err == nil {
		t.Fatal("token proof accepted")
	}
}

func TestBoardRepoChoicesForTheBoardServerOnly(t *testing.T) {
	fx := newWorkFx(t)
	fx.mux.HandleFunc("GET /api/board-repo-choices", fx.api.HandleBoardRepoChoices)
	get := func(tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/board-repo-choices", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		return rec
	}
	for _, tok := range []string{fx.token(topicOwner, "human"), fx.sessTok, "nope"} {
		if rec := get(tok); rec.Code != http.StatusUnauthorized {
			t.Fatalf("non-board caller = %d", rec.Code)
		}
	}
	if rec := get(topicDaemonToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("board server = %d %s", rec.Code, rec.Body.String())
	}
}

func TestBoardRepoChoicesNameReposAsBoardsStoreThem(t *testing.T) {
	hub := NewHub()
	d1 := &DaemonConn{ID: newUUID(), Name: "desk"}
	d1.SetSandboxClones(true)
	d1.SetCheckedOutRepos([]string{"entertainment", "scratch", "shared", "split", "has space"})
	d1.SetRepoRemotes(map[string]string{"entertainment": "example-org/example-repo", "shared": "example-org/shared", "split": "example-org/split-a"})
	hub.Register(d1)
	d2 := &DaemonConn{ID: newUUID(), Name: "laptop"}
	d2.SetSandboxClones(true)
	d2.SetCheckedOutRepos([]string{"shared", "split"})
	d2.SetRepoRemotes(map[string]string{"shared": "example-org/shared", "split": "example-org/split-b"})
	hub.Register(d2)
	pool := connectSrvTestDB(t)
	api := NewAPI(hub, pool, "board-key", nil, "")
	fixtureOwnDesktop(t, pool, d1, "user-1")
	fixtureOwnDesktop(t, pool, d2, "user-1")
	proof := workspaceBoardProof(t, api, "user-1")
	req := httptest.NewRequest(http.MethodGet, "/api/board-repo-choices", nil)
	req.Header.Set("Authorization", "Bearer board-key")
	req.Header.Set(desktopViewerHeader, proof)
	rec := httptest.NewRecorder()
	api.HandleBoardRepoChoices(rec, req)
	var out struct {
		Repos []boardRepoChoice `json:"repos"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := map[string]string{}
	for _, c := range out.Repos {
		got[c.Repo] = c.Folder + ":" + strings.Join(c.Desktops, ",")
	}
	want := map[string]string{"example-org/example-repo": "entertainment:desk", "scratch": "scratch:desk", "example-org/shared": "shared:desk,laptop", "split": "split:desk,laptop"}
	if len(got) != len(want) {
		t.Fatalf("choices %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("choices %v, want %v", got, want)
		}
	}
}

// Each of approval's own checks refuses before anything is created, with its own reason.
func TestBoardProposalApprovalChecks(t *testing.T) {
	t.Run("changed arguments", func(t *testing.T) {
		fx, c := newBoardCreateFx(t)
		p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch"})
		changed := strings.Replace(string(p.Arguments), "Launch", "Lunch!", 1)
		rec := filingRequest(fx, http.MethodPost, "/api/proposals/"+p.ID+"/approve", fx.token(topicOwner, "human"), map[string]any{"expected_arguments_raw": changed})
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "reload it and review it") || c.count() != 0 {
			t.Fatalf("changed arguments = %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("past the deadline", func(t *testing.T) {
		fx, c := newBoardCreateFx(t)
		p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch"})
		if _, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET created_at = now() - interval '8 days' WHERE id = $1`, p.ID); err != nil {
			t.Fatal(err)
		}
		rec := approveBoard(fx, fx.token(topicOwner, "human"), p, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "expired") || c.count() != 0 {
			t.Fatalf("expired = %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("sign-in confined to one board", func(t *testing.T) {
		fx, c := newBoardCreateFx(t)
		p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch"})
		confined := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: topicOwner, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Project: "board-a",
			Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
		rec := approveBoard(fx, confined, p, nil)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "confined to one board") || c.count() != 0 {
			t.Fatalf("confined = %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("Board address changed", func(t *testing.T) {
		fx, c := newBoardCreateFx(t)
		p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch"})
		if _, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET url_snapshot = 'https://other-board.example.test' WHERE id = $1`, p.ID); err != nil {
			t.Fatal(err)
		}
		rec := approveBoard(fx, fx.token(topicOwner, "human"), p, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Board destination changed") || c.count() != 0 {
			t.Fatalf("snapshot = %d %s", rec.Code, rec.Body.String())
		}
	})
}

// Board's "already used" is final: the proposal is failed, not put back to wait for another approval.
func TestBoardProposalAlreadyUsedApprovalFails(t *testing.T) {
	fx, c := newBoardCreateFx(t)
	c.err = errBoardApprovalUsed
	p := queueBoard(t, fx, fx.sess, fx.sessTok, map[string]any{"name": "Launch"})
	rec := approveBoard(fx, fx.token(topicOwner, "human"), p, nil)
	done, _ := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, p.ID)
	if rec.Code != 200 || done.State != db.ProposalFailed || !strings.Contains(string(done.Result), "already used") {
		t.Fatalf("used = %d, state %s %s", rec.Code, done.State, done.Result)
	}
	if levelOf(t, fx, fx.sess, "board-a") != db.BoardLevelPropose {
		t.Fatal("access given without a board")
	}
}
