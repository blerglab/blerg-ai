package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBlackboardRound6ClientsCannotForgeAssertions(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, Version: 1}
	record.WorkspaceID = fx.sess
	record.Milestones = []blackboard.Milestone{{ID: "m", Owner: record.Owner, Stage: "Finalizing", Version: 1}}
	if err := fx.api.blackboardStore().Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	for _, mid := range []string{"", "m"} {
		r := httptest.NewRequest("POST", "/api/blackboards/"+record.ID+"/events?session="+fx.sess, strings.NewReader(`{"kind":"assertion","milestone_id":"`+mid+`","payload":{"operation":"plan_commit","accepted":true}}`))
		r.Header.Set("Authorization", "Bearer "+fx.sessTok)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Errorf("client assertion accepted in scope %q: %d", mid, w.Code)
		}
	}
	got, err := fx.api.blackboardStore().Load(ctx, record.ID)
	if err != nil || len(got.Events) != 0 {
		t.Fatalf("client assertions persisted: %d %v", len(got.Events), err)
	}
	r := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/plan?blackboard="+record.ID+"&milestone=m&operation=commit", nil)
	r.Header.Set("Authorization", "Bearer "+fx.sessTok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("refused commit: %d", w.Code)
	}
	got, err = fx.api.blackboardStore().Load(ctx, record.ID)
	if err != nil || len(got.Events) != 1 || got.Events[0].Kind != "assertion" || got.Events[0].AuthenticatedActor.ID != fx.sess {
		t.Fatalf("internal Finalizing assertion not preserved: %v", err)
	}
	var assertion struct {
		Operation string
		Accepted  *bool
	}
	if err := json.Unmarshal(got.Events[0].Payload, &assertion); err != nil || assertion.Operation != "plan_commit" || assertion.Accepted == nil || *assertion.Accepted {
		t.Fatal("internal assertion payload was not the fixed refusal")
	}
}

func TestBlackboardRound6ReadAndRefusalSkipBusyAdmission(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	human := fx.token(topicOwner, "human")
	if code, _ := setLevel(fx, human, "board-a", "read"); code != 200 {
		t.Fatal("board grant", code)
	}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: &blackboardGateway{}})
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}, Milestones: []blackboard.Milestone{{ID: "m", Owner: owner, BoardID: "board-a", ReleaseID: "release", Version: 1}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := fx.api.blackboardStore()
	if err := store.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- store.UpdatePartition(ctx, record.ID, blackboard.Partition{Kind: blackboard.PartitionEvent, ActorKind: "agent", ActorID: "busy"}, func(*blackboard.Record) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("writer did not enter")
	}
	defer func() { close(release); <-writerDone }()
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/api/blackboards/" + record.ID + "/milestones/m/slice?session=" + fx.sess, human, 200},
		{"POST", "/api/sessions/" + fx.sess + "/plan?operation=commit&blackboard=" + record.ID + "&milestone=m", fx.sessTok, 403},
	} {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest(tc.method, tc.path, nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			done <- w
		}()
		for {
			select {
			case w := <-done:
				if w.Code != tc.status {
					t.Fatalf("busy route %s: %d %s", tc.method, w.Code, w.Body.String())
				}
				goto completed
			case <-ctx.Done():
				t.Fatal("read/refusal blocked behind automatic writer")
			default:
				if n := store.AutomaticWriteWaiters(record.ID); n != 0 {
					t.Fatal("read/refusal joined automatic queue")
				}
				runtime.Gosched()
			}
		}
	completed:
	}
}
