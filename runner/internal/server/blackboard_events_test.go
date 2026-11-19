package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

func TestBlackboardEventPersonAndAgentProvenance(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	human := fx.token(topicOwner, "human")
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1, Milestones: []blackboard.Milestone{{ID: blackboard.NewID(), Owner: owner, Version: 1}}}
	if err := api.blackboardStore().Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	request := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/blackboards/"+record.ID+"/events?session="+fx.sess, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	rec := request(human, `{"kind":"decision","payload":{"reason":"Try this"}}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"source":"person"`) {
		t.Fatalf("person %d %s", rec.Code, rec.Body.String())
	}
	body, _ := json.Marshal(blackboard.EventInput{MilestoneID: record.Milestones[0].ID, Kind: "alpha", Payload: []byte(`{"commit":"abc","by":"person"}`)})
	rec = request(fx.sessTok, string(body))
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"source":"agent"`) || !strings.Contains(rec.Body.String(), `"number":1`) {
		t.Fatalf("agent %d %s", rec.Code, rec.Body.String())
	}
	rec = request(fx.sessTok, `{"kind":"plan_set","payload":{"by":"person"}}`)
	if rec.Code != 400 {
		t.Fatalf("plan authority %d %s", rec.Code, rec.Body.String())
	}
	api.snapshotBlackboards(context.Background())
	api.snapshotBlackboards(context.Background())
	stored, err := api.blackboardStore().Load(context.Background(), record.ID)
	if err != nil || len(stored.Snapshots) != 1 || !stored.Snapshots[0].Complete {
		t.Fatalf("job %+v %v", stored.Snapshots, err)
	}
}
