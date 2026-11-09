package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBlackboardRoutesRequireAuthentication(t *testing.T) {
	api, _, _ := newLiveCardAPI(t)
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	for _, path := range []struct{ method, path string }{{"GET", "/api/blackboards/b"}, {"POST", "/api/blackboards/b/proposals"}, {"POST", "/api/blackboards/b/commit"}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(path.method, path.path, nil))
		if rec.Code != 401 {
			t.Fatalf("%s: %d", path.path, rec.Code)
		}
	}
}

func TestBlackboardPlanHTTPAuthorityAndRevocation(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	human := fx.token(topicOwner, "human")
	if code, body := setLevel(fx, human, "board-a", "read"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	board := &liveCardBoard{column: "col-inbox"}
	api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path+"?session="+fx.sess, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	actor := blackboard.Actor{Kind: "human", ID: topicOwner}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: actor, WorkspaceID: fx.sess, Name: "Plan", Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}, Goals: []blackboard.Goal{{ID: "goal", Owner: actor, Name: "Purpose", Version: 1}}, Milestones: []blackboard.Milestone{{ID: "milestone", Owner: actor, Name: "Next", BoardID: "board-a", ReleaseID: "release", Version: 1}}, Thrusts: []blackboard.Thrust{{ID: "thrust", Owner: actor, Name: "Work", GoalID: "goal", MilestoneID: "milestone", Version: 1}}}
	if err := api.blackboardStore().Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	var rec *httptest.ResponseRecorder
	path := "/api/blackboards/" + record.ID
	body, _ := json.Marshal(map[string]any{"milestone_id": record.Milestones[0].ID, "changes": []blackboard.Change{{ID: "selected", Operation: "add", Item: blackboard.Item{BoardID: "board-a", CardNumber: 1, ThrustID: record.Thrusts[0].ID, Reason: "Bug"}}}})
	rec = request("POST", path+"/proposals", human, string(body))
	if rec.Code != 201 {
		t.Fatalf("propose %d %s", rec.Code, rec.Body.String())
	}
	var proposal struct {
		ID blackboard.ProposalID `json:"proposal_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &proposal)
	body, _ = json.Marshal(map[string]any{"milestone_id": record.Milestones[0].ID, "proposal_id": proposal.ID, "revision": 0, "selected": []string{"selected"}})
	rec = request("POST", path+"/commit", fx.token(topicOwner, "agent"), string(body))
	if rec.Code != 403 {
		t.Fatalf("agent commit %d %s", rec.Code, rec.Body.String())
	}
	rec = request("POST", path+"/commit", human, string(body))
	if rec.Code != 410 {
		t.Fatalf("retired commit %d %s", rec.Code, rec.Body.String())
	}
	board.mu.Lock()
	board.refuse = true
	board.mu.Unlock()
	rec = request("GET", path, human, "")
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "Purpose") {
		t.Fatalf("revoked read %d %s", rec.Code, rec.Body.String())
	}
}

func TestBlackboardPlainWorkspaceHTTP(t *testing.T) {
	fx, board, _ := newBoardLevelFx(t)
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	human := fx.token(topicOwner, "human")
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path+"?session="+fx.sess, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	actor := blackboard.Actor{Kind: "human", ID: topicOwner}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: actor, WorkspaceID: fx.sess, Name: "Research", Version: 1, Goals: []blackboard.Goal{{ID: "goal", Owner: actor, Name: "Understand the problem", Version: 1}}}
	if err := api.blackboardStore().Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	var rec *httptest.ResponseRecorder
	path := "/api/blackboards/" + record.ID
	for revision := 0; revision < 2; revision++ {
		body, _ := json.Marshal(map[string]any{"revision": revision, "changes": []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "Read one paper", GoalID: record.Goals[0].ID}}}})
		rec = request("POST", path+"/plan", human, string(body))
		if rec.Code != 410 {
			t.Fatalf("apply %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = request("GET", path+"/slice", human, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cards":[]`) {
		t.Fatalf("plain slice %d %s", rec.Code, rec.Body.String())
	}
	rec = request("GET", "/api/blackboards", human, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), record.ID) {
		t.Fatalf("list %d %s", rec.Code, rec.Body.String())
	}
	rec = request("POST", path+"/plan", fx.token(topicOwner, "agent"), `{"revision":2,"changes":[]}`)
	if rec.Code != 403 {
		t.Fatalf("agent apply %d", rec.Code)
	}
	if board.count() != 0 {
		t.Fatal("plain workspace contacted board")
	}
	stored, _ := api.blackboardStore().Load(context.Background(), record.ID)
	if len(stored.Milestones) != 0 || len(stored.Links) != 0 || stored.PlanRevision != 0 || len(stored.Proposals) != 0 {
		t.Fatalf("unused levels/approval queued: %+v", stored)
	}
}
