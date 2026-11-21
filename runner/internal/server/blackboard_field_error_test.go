package server

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

func TestBlackboardProposalFieldError(t *testing.T) {
	store := blackboard.NewMemoryStore()
	owner := blackboard.Actor{ID: "owner", Kind: "human"}
	if err := store.Create(t.Context(), blackboard.Record{ID: "record", Owner: owner, Version: 1, Milestones: []blackboard.Milestone{{ID: "m", BoardID: "board", Version: 1}}}); err != nil {
		t.Fatal(err)
	}
	_, err := (blackboard.Service{Store: store, BlackboardID: "record"}).Propose(t.Context(), blackboard.Actor{ID: "main", Kind: "agent"}, "m", []blackboard.Change{{Operation: "move", Item: blackboard.Item{BoardID: "board", CardNumber: 1, Reason: "CANARY_PRIVATE"}}})
	if err == nil {
		t.Fatal("missing destination accepted")
	}
	var logs bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = old })
	w := httptest.NewRecorder()
	blackboardError(w, err)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "item.destination: required for move") {
		t.Fatalf("field refusal: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String()+logs.String(), "CANARY_PRIVATE") {
		t.Fatal("caller data escaped in diagnostics")
	}
	if logs.Len() == 0 {
		t.Fatal("refusal was not logged")
	}
}
