package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestBlackboardMilestoneAddBoundAsk(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	frozen := time.Now().UTC()
	r := blackboard.Record{Milestones: []blackboard.Milestone{{ID: "previous", Name: "0.1.0", Owner: owner, Version: 1, FrozenAt: &frozen}}, ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1}
	if err := fx.api.blackboardStore().Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
	call := func(path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	proposal := call("/api/sessions/"+fx.sess+"/plan?operation=propose&blackboard="+r.ID, fx.sessTok, `{"changes":[{"operation":"milestone_add","milestone":{"name":"0.2.0","target":"2027-01-02","description":"Next work"}}]}`)
	if proposal.Code != 201 {
		t.Fatalf("propose: %d %s", proposal.Code, proposal.Body)
	}
	var wrapper struct {
		Data struct {
			ID string `json:"proposal_id"`
		} `json:"untrusted_data"`
	}
	if err := json.Unmarshal(proposal.Body.Bytes(), &wrapper); err != nil {
		t.Fatal(err)
	}
	ask := call("/api/messages", fx.sessTok, `{"session_id":"`+fx.sess+`","kind":"ask","body":"Open next milestone?","person_required":true,"subject":{"kind":"plan","blackboard_id":"`+r.ID+`","proposal_id":"`+wrapper.Data.ID+`","revision":0}}`)
	if ask.Code != 201 {
		t.Fatalf("ask: %d %s", ask.Code, ask.Body)
	}
	messages, err := db.ListMessagesBySession(t.Context(), fx.pool, fx.sess)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages: %v %v", messages, err)
	}
	msg := messages[0]
	for _, want := range []string{"Add milestone: 0.2.0", "Target: 2027-01-02", "Description: Next work"} {
		if !strings.Contains(msg.Body, want) {
			t.Fatalf("missing %s: %s", want, msg.Body)
		}
	}
	before, _ := fx.api.blackboardStore().Load(t.Context(), r.ID)
	if len(before.Milestones) != 1 {
		t.Fatal("proposal changed scope")
	}
	if bad := call("/api/messages/"+msg.ID+"/reply", fx.token(topicOther, "human"), `{"answer":"yes"}`); bad.Code < 400 {
		t.Fatal("nonowner committed")
	}
	before, _ = fx.api.blackboardStore().Load(t.Context(), r.ID)
	if len(before.Milestones) != 1 {
		t.Fatal("unauthorized reply changed scope")
	}
	good := call("/api/messages/"+msg.ID+"/reply", fx.token(topicOwner, "human"), `{"answer":"yes"}`)
	if good.Code != 200 {
		t.Fatalf("owner: %d %s", good.Code, good.Body)
	}
	after, err := fx.api.blackboardStore().Load(t.Context(), r.ID)
	if err != nil || len(after.Milestones) != 2 {
		t.Fatalf("missing milestone: %+v %v", after.Milestones, err)
	}
	m := after.Milestones[1]
	if m.Name != "0.2.0" || m.Description != "Next work" || m.Target == nil || after.PlanRevision != 1 {
		t.Fatalf("wrong milestone: %+v", m)
	}
}
