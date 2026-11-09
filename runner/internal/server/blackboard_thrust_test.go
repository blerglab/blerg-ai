package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestBlackboardThrustBoundDecisions(t *testing.T) {
	for _, scenario := range []string{"thrust_add", "thrust_rename", "thrust_merge", "thrust_remove", "nonempty", "became_nonempty", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			operation := scenario
			if scenario == "nonempty" || scenario == "became_nonempty" {
				operation = "thrust_remove"
			}
			if scenario == "stale" {
				operation = "thrust_rename"
			}
			fx, _, _ := newBoardLevelFx(t)
			actor := blackboard.Actor{ID: topicOwner, Kind: "human"}
			r := blackboard.Record{ID: blackboard.NewID(), Owner: actor, WorkspaceID: fx.sess, Version: 1,
				Goals:   []blackboard.Goal{{ID: "goal", Name: "Reliable work", Owner: actor, Version: 1}},
				Thrusts: []blackboard.Thrust{{ID: "a", Name: "Original", GoalID: "goal", Owner: actor, Version: 1}, {ID: "b", Name: "Destination", GoalID: "goal", Owner: actor, Version: 1}}}
			if operation == "thrust_merge" || scenario == "nonempty" {
				r.Items = []blackboard.Item{{ID: "card", ThrustID: "a", State: "planned"}}
			}
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
			payload := `{"id":"a","reason":"Group related work"}`
			switch operation {
			case "thrust_add":
				payload = `{"name":"New theme","goal_id":"goal","reason":"Group related work"}`
			case "thrust_rename":
				payload = `{"id":"a","name":"Better name","reason":"Group related work"}`
			case "thrust_merge":
				payload = `{"id":"a","destination":"b","reason":"Group related work"}`
			}
			proposal := call("/api/sessions/"+fx.sess+"/plan?operation=propose&blackboard="+r.ID, fx.sessTok, `{"changes":[{"operation":"`+operation+`","thrust":`+payload+`}]}`)
			if scenario == "nonempty" {
				if proposal.Code != 400 {
					t.Fatalf("nonempty: %d %s", proposal.Code, proposal.Body)
				}
				return
			}
			if proposal.Code != 201 {
				t.Fatalf("propose: %d %s", proposal.Code, proposal.Body)
			}
			var q struct {
				ID string `json:"proposal_id"`
			}
			var wrapper struct {
				Data json.RawMessage `json:"untrusted_data"`
			}
			_ = json.Unmarshal(proposal.Body.Bytes(), &wrapper)
			_ = json.Unmarshal(wrapper.Data, &q)
			ask := call("/api/messages", fx.sessTok, `{"session_id":"`+fx.sess+`","kind":"ask","body":"Commit themes?","person_required":true,"subject":{"kind":"plan","blackboard_id":"`+r.ID+`","proposal_id":"`+q.ID+`","revision":0}}`)
			if ask.Code != 201 {
				t.Fatalf("ask: %d %s", ask.Code, ask.Body)
			}
			messages, err := db.ListMessagesBySession(t.Context(), fx.pool, fx.sess)
			if err != nil || len(messages) != 1 {
				t.Fatalf("messages: %v %v", messages, err)
			}
			message := messages[0]
			if !strings.Contains(message.Body, "Group related work") || strings.Contains(message.Body, "Card #0") {
				t.Fatalf("question: %s", message.Body)
			}
			for _, want := range map[string][]string{
				"thrust_add":    {"Add thrust: New theme", "Goal: Reliable work"},
				"thrust_rename": {"Rename thrust: Original → Better name"},
				"thrust_merge":  {"Merge thrust: Original → Destination", "Moves all cards"},
				"thrust_remove": {"Remove thrust: Original"},
			}[operation] {
				if !strings.Contains(message.Body, want) {
					t.Fatalf("question missing %q: %s", want, message.Body)
				}
			}
			before, _ := fx.api.blackboardStore().Load(t.Context(), r.ID)
			if len(before.Thrusts) != 2 || before.PlanRevision != 0 {
				t.Fatal("proposal changed scope")
			}
			reply := func(token string) *httptest.ResponseRecorder {
				return call("/api/messages/"+message.ID+"/reply", token, `{"answer":"yes"}`)
			}
			if bad := reply(fx.token(topicOther, "human")); bad.Code < 400 {
				t.Fatalf("non-owner: %d", bad.Code)
			}
			if scenario == "became_nonempty" || scenario == "stale" {
				if err := fx.api.blackboardStore().Update(t.Context(), r.ID, func(r *blackboard.Record) error {
					if scenario == "stale" {
						r.PlanRevision++
					} else {
						r.Items = append(r.Items, blackboard.Item{ID: "late-card", ThrustID: "a", State: "planned"})
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				refused := reply(fx.token(topicOwner, "human"))
				if refused.Code < 400 {
					t.Fatalf("%s approved: %d %s", scenario, refused.Code, refused.Body)
				}
				after, _ := fx.api.blackboardStore().Load(t.Context(), r.ID)
				if len(after.Thrusts) != 2 || after.Thrusts[0].Name != "Original" {
					t.Fatal("refusal changed scope")
				}
				return
			}
			good := reply(fx.token(topicOwner, "human"))
			if good.Code != 200 {
				t.Fatalf("owner: %d %s", good.Code, good.Body)
			}
			after, err := fx.api.blackboardStore().Load(t.Context(), r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.PlanRevision != 1 {
				t.Fatalf("revision: %+v", after)
			}
			switch operation {
			case "thrust_add":
				if len(after.Thrusts) != 3 {
					t.Fatal("missing new thrust")
				}
			case "thrust_rename":
				if after.Thrusts[0].Name != "Better name" {
					t.Fatal("not renamed")
				}
			case "thrust_merge":
				if len(after.Thrusts) != 1 || after.Items[0].ThrustID != "b" {
					t.Fatal("not merged")
				}
			case "thrust_remove":
				if len(after.Thrusts) != 1 {
					t.Fatal("not removed")
				}
			}
			var count int
			if err = fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM thrusts WHERE blackboard_id=$1`, r.ID).Scan(&count); err != nil || count != len(after.Thrusts) {
				t.Fatalf("thrust index: %d %v", count, err)
			}
		})
	}
}
