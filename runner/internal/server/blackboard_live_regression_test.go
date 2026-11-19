package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

func TestBlackboardBrowserReadsWorkspaceAddedMilestone(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	actor := blackboard.Actor{Kind: "human", ID: topicOwner}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: actor, WorkspaceID: fx.sess, Version: 1, Goals: []blackboard.Goal{{ID: "goal", Owner: actor, Name: "Reliable sessions", Version: 1}}, Milestones: []blackboard.Milestone{{ID: "old", Owner: actor, Name: "0.6.0", Version: 1}}}
	store := fx.api.blackboardStore()
	if err := store.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	service := blackboard.Service{Store: store, BlackboardID: record.ID}
	person := blackboard.Person{Actor: actor, LoginID: "login"}
	q, err := service.Propose(t.Context(), blackboard.Actor{ID: "main", Kind: "agent"}, "", []blackboard.Change{{ID: "open", Operation: "milestone_add", Milestone: &blackboard.MilestoneChange{Name: "0.7.0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Decide(t.Context(), person, "", q, 0, []blackboard.ItemID{"open"}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	mid := stored.Milestones[1].ID
	if err = service.Apply(t.Context(), person, mid, 0, []blackboard.Change{{Operation: "thrust_add", Thrust: &blackboard.ThrustChange{Name: "Recovery", GoalID: "goal", Reason: "Reliable sessions"}}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	for _, cursor := range []string{"", "&cursor=1"} {
		req := httptest.NewRequest("GET", "/api/blackboards/"+record.ID+"?session="+fx.sess+"&limit=1"+cursor, nil)
		req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		var body struct {
			Record blackboard.Record `json:"record"`
			Next   string            `json:"next_cursor"`
		}
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &body) != nil {
			t.Fatalf("read: %d %s", out.Code, out.Body.String())
		}
		want := "0.6.0"
		if cursor != "" {
			want = "0.7.0"
		}
		if len(body.Record.Milestones) != 1 || body.Record.Milestones[0].Name != want {
			t.Fatalf("missing %s: %+v", want, body.Record.Milestones)
		}
		if cursor == "" && body.Next != "1" {
			t.Fatalf("missing continuation: %q", body.Next)
		}
		if cursor == "" && (len(body.Record.Thrusts) != 1 || body.Record.Thrusts[0].MilestoneID != mid) {
			t.Fatalf("missing new thrust: %+v", body.Record.Thrusts)
		}
	}
}
